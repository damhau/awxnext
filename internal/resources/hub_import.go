package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
)

const (
	maxManifestBytes = 4 << 20
	maxRuntimeBytes  = 1 << 20
	// Bounds the work of reading a collection's artifact: past this many
	// uncompressed bytes, what it lacks is missing.
	maxArtifactScan = 1 << 30
)

// semverRe is semver.org's: ansible-galaxy requires semantic versions.
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// CollectionArtifact is what the hub reads from a collection's tarball.
type CollectionArtifact struct {
	Namespace, Name, Version string
	RequiresAnsible          string
	// Info is MANIFEST.json's collection_info.
	Info     map[string]any
	Manifest map[string]any
	Files    map[string]any
}

// ReadCollectionArtifact reads the MANIFEST.json, FILES.json and
// meta/runtime.yml of a collection tarball, as ansible-galaxy builds it.
// Nothing is written to disk.
func ReadCollectionArtifact(data []byte) (*CollectionArtifact, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("the artifact is not a gzip'd tarball")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxArtifactScan))
	var manifest, files []byte
	var runtime []byte
	for manifest == nil || files == nil || runtime == nil {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the artifact: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		var dst *[]byte
		limit := int64(maxManifestBytes)
		switch strings.TrimPrefix(h.Name, "./") {
		case "MANIFEST.json":
			dst = &manifest
		case "FILES.json":
			dst = &files
		case "meta/runtime.yml":
			dst, limit = &runtime, maxRuntimeBytes
		default:
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", h.Name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s is larger than %d bytes", h.Name, limit)
		}
		*dst = b
	}
	if manifest == nil {
		return nil, errors.New("the artifact has no MANIFEST.json")
	}
	a := &CollectionArtifact{}
	if err := json.Unmarshal(manifest, &a.Manifest); err != nil {
		return nil, fmt.Errorf("MANIFEST.json: %w", err)
	}
	a.Info, _ = a.Manifest["collection_info"].(map[string]any)
	if a.Info == nil {
		return nil, errors.New("MANIFEST.json has no collection_info")
	}
	delete(a.Manifest, "collection_info")
	if files != nil {
		if err := json.Unmarshal(files, &a.Files); err != nil {
			return nil, fmt.Errorf("FILES.json: %w", err)
		}
	}
	if runtime != nil {
		var meta struct {
			RequiresAnsible string `yaml:"requires_ansible"`
		}
		if yaml.Unmarshal(runtime, &meta) == nil {
			a.RequiresAnsible = meta.RequiresAnsible
		}
	}
	a.Namespace, a.Name, a.Version = str(a.Info["namespace"]), str(a.Info["name"]), str(a.Info["version"])
	switch {
	case !validGalaxyName(a.Namespace):
		return nil, fmt.Errorf("invalid namespace %q", a.Namespace)
	case !validGalaxyName(a.Name):
		return nil, fmt.Errorf("invalid collection name %q", a.Name)
	case !semverRe.MatchString(a.Version):
		return nil, fmt.Errorf("version %q is not a semantic version", a.Version)
	}
	if deps, ok := a.Info["dependencies"]; ok && deps != nil {
		if _, ok := deps.(map[string]any); !ok {
			return nil, errors.New("collection_info's dependencies is not a mapping")
		}
	} else {
		a.Info["dependencies"] = map[string]any{}
	}
	return a, nil
}

// ImportCollection stores a collection tarball as a version of its
// collection, in its namespace, and adds it to a repository. The namespace
// must exist. It returns the version's id.
func ImportCollection(ctx context.Context, app *api.App, repository string, data []byte) (int64, error) {
	a, err := ReadCollectionArtifact(data)
	if err != nil {
		return 0, err
	}
	var nsID, repoID int64
	if err := app.DB.QueryRow(ctx, "SELECT id FROM collection_namespaces WHERE name = $1", a.Namespace).Scan(&nsID); err != nil {
		return 0, fmt.Errorf("no namespace %s", a.Namespace)
	}
	if err := app.DB.QueryRow(ctx, "SELECT id FROM collection_repositories WHERE name = $1", repository).Scan(&repoID); err != nil {
		return 0, fmt.Errorf("no repository %s", repository)
	}
	var exists bool
	app.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		WHERE c.namespace_id = $1 AND c.name = $2 AND v.version = $3)`, nsID, a.Name, a.Version).Scan(&exists)
	if exists {
		return 0, versionExists(a)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	filename := fmt.Sprintf("%s-%s-%s.tar.gz", a.Namespace, a.Name, a.Version)
	kind := app.Settings.String(ctx, "HUB_STORAGE")
	if kind == "" {
		kind = "database"
	}
	location := ""
	if kind != "database" {
		store, err := hubStore(app)(ctx, kind)
		if err != nil {
			return 0, err
		}
		var install string
		app.DB.QueryRow(ctx, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&install)
		key := app.Settings.String(ctx, "HUB_STORAGE_PREFIX") + install + "/" + digest + "/" + filename
		if location, err = store.Put(ctx, key, data); err != nil {
			return 0, fmt.Errorf("storing the artifact: %w", err)
		}
	}
	var id int64
	err = pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		var collID int64
		if err := tx.QueryRow(ctx, `INSERT INTO collections (namespace_id, name) VALUES ($1, $2)
			ON CONFLICT (namespace_id, name) DO UPDATE SET modified = now() RETURNING id`, nsID, a.Name).Scan(&collID); err != nil {
			return err
		}
		info, _ := json.Marshal(a.Info)
		manifest, _ := json.Marshal(a.Manifest)
		files, _ := json.Marshal(a.Files)
		err := tx.QueryRow(ctx, `INSERT INTO collection_versions (collection_id, version, requires_ansible, metadata, manifest, files,
			filename, sha256, size, storage, location) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (collection_id, version) DO NOTHING RETURNING id`,
			collID, a.Version, a.RequiresAnsible, info, manifest, files, filename, digest, int64(len(data)), kind, location).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return versionExists(a)
		}
		if err != nil {
			return err
		}
		if kind == "database" {
			_, err = tx.Exec(ctx, "INSERT INTO collection_version_data (version_id, data) VALUES ($1, $2)", id, data)
		} else {
			// The same bytes deleted earlier and not yet removed: keep them.
			_, err = tx.Exec(ctx, "DELETE FROM collection_object_deletions WHERE storage = $1 AND location = $2", kind, location)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO collection_repository_contents (repository_id, version_id) VALUES ($1, $2)", repoID, id)
		return err
	})
	return id, err
}

func versionExists(a *CollectionArtifact) error {
	return api.BadRequest(fmt.Sprintf("%s.%s %s already exists.", a.Namespace, a.Name, a.Version))
}

// semver is a parsed semantic version, ordered as semver.org says.
type semver struct {
	core [3]uint64
	pre  []string
}

func parseSemver(s string) (semver, bool) {
	var v semver
	if !semverRe.MatchString(s) {
		return v, false
	}
	s, _, _ = strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(s, "-")
	for i, p := range strings.SplitN(core, ".", 3) {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

// compareSemver orders versions; ones that don't parse sort first, by text.
func compareSemver(a, b string) int {
	va, oka := parseSemver(a)
	vb, okb := parseSemver(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := range va.core {
		if va.core[i] != vb.core[i] {
			if va.core[i] < vb.core[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0
	case len(va.pre) == 0:
		return 1
	case len(vb.pre) == 0:
		return -1
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := comparePrerelease(va.pre[i], vb.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1
	case len(va.pre) > len(vb.pre):
		return 1
	}
	return 0
}

func comparePrerelease(a, b string) int {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
		return 0
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

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
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
)

const (
	maxManifestBytes = 4 << 20
	maxRuntimeBytes  = 1 << 20
	// galaxy-importer's limit.
	maxCollectionTags = 20
	// Links followed from a FILES.json entry to the file it names.
	maxLinkHops = 40
)

// Bound the work of reading a collection's artifact: its size once
// decompressed, and its members. Tests lower them.
var (
	maxArtifactBytes   int64 = 1 << 30
	maxArtifactMembers       = 50000
)

// semverRe is semver.org's: ansible-galaxy requires semantic versions.
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// tagRe is galaxy-importer's rule for tags.
var tagRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// CollectionArtifact is what the hub reads from a collection's tarball.
type CollectionArtifact struct {
	Namespace, Name, Version string
	RequiresAnsible          string
	// Info is MANIFEST.json's collection_info.
	Info     map[string]any
	Manifest map[string]any
	Files    map[string]any
	// ManifestJSON is MANIFEST.json as the artifact holds it: what its
	// signatures sign.
	ManifestJSON []byte
}

// ImportMessage is a line of an import's log, as galaxy_ng's.
type ImportMessage struct {
	Level   string  `json:"level"`
	Message string  `json:"message"`
	Time    float64 `json:"time"`
}

// ImportLog collects the messages of an import, each check's result.
type ImportLog struct{ Messages []ImportMessage }

func (l *ImportLog) add(level, format string, args ...any) {
	if l == nil {
		return
	}
	l.Messages = append(l.Messages, ImportMessage{Level: level, Message: fmt.Sprintf(format, args...),
		Time: float64(time.Now().UnixMicro()) / 1e6})
}

func (l *ImportLog) info(format string, args ...any) { l.add("INFO", format, args...) }
func (l *ImportLog) warn(format string, args ...any) { l.add("WARNING", format, args...) }

// ImportError is why an import failed, with a code as galaxy_ng's.
type ImportError struct{ Code, Message string }

func (e *ImportError) Error() string { return e.Message }

func importErr(code, format string, args ...any) error {
	return &ImportError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// artifactMember is a member of a collection's tarball: a regular file
// with its checksum, a directory, or a link to another member.
type artifactMember struct {
	typ  byte
	sum  string
	link string
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// memberPath is where a member's name puts it in the collection; false
// when that would be outside it.
func memberPath(name string) (string, bool) {
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	return path.Clean(name), true
}

// ReadCollectionArtifact reads and checks a collection tarball, as
// ansible-galaxy builds it. Nothing is written to disk.
func ReadCollectionArtifact(data []byte) (*CollectionArtifact, error) {
	return checkCollectionArtifact(data, nil)
}

// checkCollectionArtifact reads every member of a collection tarball and
// checks its layout, MANIFEST.json and FILES.json with their checksums,
// and the metadata, logging each check.
func checkCollectionArtifact(data []byte, log *ImportLog) (*CollectionArtifact, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, importErr("invalid_artifact", "The artifact is not a gzip'd tarball.")
	}
	defer gz.Close()
	cr := &countingReader{r: io.LimitReader(gz, maxArtifactBytes+1)}
	tr := tar.NewReader(cr)
	tooLarge := func() error {
		return importErr("invalid_artifact", "The artifact is larger than %s decompressed.", humanBytes(maxArtifactBytes))
	}
	members := map[string]*artifactMember{}
	var manifest, files, runtime []byte
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cr.n > maxArtifactBytes {
				return nil, tooLarge()
			}
			return nil, importErr("invalid_artifact", "Reading the artifact: %v.", err)
		}
		if count >= maxArtifactMembers {
			return nil, importErr("invalid_artifact", "The artifact has more than %d members.", maxArtifactMembers)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		p, ok := memberPath(h.Name)
		if !ok {
			return nil, importErr("invalid_artifact", "The member %q is outside the collection.", h.Name)
		}
		if p == "." {
			continue
		}
		if prev := members[p]; prev != nil && (prev.typ != tar.TypeDir || h.Typeflag != tar.TypeDir) {
			return nil, importErr("invalid_artifact", "%s is twice in the artifact.", p)
		}
		m := &artifactMember{typ: h.Typeflag}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeSymlink:
			if path.IsAbs(h.Linkname) {
				return nil, importErr("invalid_artifact", "%s links outside the collection.", p)
			}
			m.link = path.Join(path.Dir(p), h.Linkname)
			if m.link == ".." || strings.HasPrefix(m.link, "../") {
				return nil, importErr("invalid_artifact", "%s links outside the collection.", p)
			}
		case tar.TypeLink:
			target, ok := memberPath(h.Linkname)
			if !ok || members[target] == nil || members[target].typ != tar.TypeReg {
				return nil, importErr("invalid_artifact", "%s is a hard link to %q, which is no file of the artifact before it.", p, h.Linkname)
			}
			m.typ, m.sum = tar.TypeReg, members[target].sum
		case tar.TypeReg:
			var dst *[]byte
			limit := int64(maxManifestBytes)
			switch p {
			case "MANIFEST.json":
				dst = &manifest
			case "FILES.json":
				dst = &files
			case "meta/runtime.yml":
				dst, limit = &runtime, maxRuntimeBytes
			}
			sum := sha256.New()
			if dst != nil {
				b, err := io.ReadAll(io.LimitReader(tr, limit+1))
				if err != nil {
					return nil, importErr("invalid_artifact", "Reading %s: %v.", p, err)
				}
				if int64(len(b)) > limit {
					return nil, importErr("invalid_artifact", "%s is larger than %s.", p, humanBytes(limit))
				}
				*dst = b
				sum.Write(b)
			} else if _, err := io.Copy(sum, tr); err != nil {
				if cr.n > maxArtifactBytes {
					return nil, tooLarge()
				}
				return nil, importErr("invalid_artifact", "Reading %s: %v.", p, err)
			}
			m.sum = hex.EncodeToString(sum.Sum(nil))
		default:
			return nil, importErr("invalid_artifact", "%s is a device, a FIFO or another kind of file a collection can't hold.", p)
		}
		members[p] = m
	}
	if cr.n > maxArtifactBytes {
		return nil, tooLarge()
	}
	switch {
	case manifest == nil:
		return nil, importErr("invalid_artifact", "The artifact has no MANIFEST.json at its root.")
	case files == nil:
		return nil, importErr("invalid_artifact", "The artifact has no FILES.json at its root.")
	}
	log.info("Layout: %d members, %s decompressed, none outside the collection.", len(members), humanBytes(cr.n))

	a := &CollectionArtifact{ManifestJSON: manifest}
	if err := json.Unmarshal(manifest, &a.Manifest); err != nil {
		return nil, importErr("invalid_artifact", "MANIFEST.json: %v.", err)
	}
	a.Info, _ = a.Manifest["collection_info"].(map[string]any)
	if a.Info == nil {
		return nil, importErr("invalid_artifact", "MANIFEST.json has no collection_info.")
	}
	delete(a.Manifest, "collection_info")
	if err := json.Unmarshal(files, &a.Files); err != nil {
		return nil, importErr("invalid_artifact", "FILES.json: %v.", err)
	}
	n, err := checkFileManifest(a, files, members)
	if err != nil {
		return nil, err
	}
	log.info("Checksums: MANIFEST.json's of FILES.json, and FILES.json's of each of the %d files, match.", n)

	if runtime != nil {
		var meta struct {
			RequiresAnsible string `yaml:"requires_ansible"`
		}
		if yaml.Unmarshal(runtime, &meta) == nil {
			a.RequiresAnsible = meta.RequiresAnsible
		}
	}
	if err := checkCollectionInfo(a, members, log); err != nil {
		return nil, err
	}
	return a, nil
}

// checkFileManifest checks MANIFEST.json's checksum of FILES.json, and
// that FILES.json lists every file of the artifact with its checksum. It
// returns how many files it checked.
func checkFileManifest(a *CollectionArtifact, files []byte, members map[string]*artifactMember) (int, error) {
	fmf, _ := a.Manifest["file_manifest_file"].(map[string]any)
	want, _ := fmf["chksum_sha256"].(string)
	if want == "" {
		return 0, importErr("checksum_mismatch", "MANIFEST.json gives no SHA-256 checksum of FILES.json (file_manifest_file).")
	}
	sum := sha256.Sum256(files)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return 0, importErr("checksum_mismatch", "FILES.json's SHA-256 is %s, MANIFEST.json says %s.", got, want)
	}
	var fm struct {
		Files []struct {
			Name       string  `json:"name"`
			Ftype      string  `json:"ftype"`
			ChksumType *string `json:"chksum_type"`
			Chksum     *string `json:"chksum_sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(files, &fm); err != nil {
		return 0, importErr("invalid_artifact", "FILES.json: %v.", err)
	}
	listed := map[string]bool{"MANIFEST.json": true, "FILES.json": true}
	checked := 0
	for _, f := range fm.Files {
		if f.Name == "." {
			continue
		}
		p, ok := memberPath(f.Name)
		if !ok {
			return 0, importErr("invalid_artifact", "FILES.json lists %q, outside the collection.", f.Name)
		}
		listed[p] = true
		if f.Ftype != "file" {
			continue
		}
		if members[p] == nil {
			return 0, importErr("checksum_mismatch", "FILES.json lists %s, which the artifact lacks.", p)
		}
		if f.ChksumType == nil || *f.ChksumType != "sha256" || f.Chksum == nil {
			return 0, importErr("checksum_mismatch", "FILES.json gives %s no SHA-256 checksum.", p)
		}
		got, err := memberSum(members, p)
		if err != nil {
			return 0, err
		}
		if !strings.EqualFold(got, *f.Chksum) {
			return 0, importErr("checksum_mismatch", "%s: its SHA-256 is %s, FILES.json says %s.", p, got, *f.Chksum)
		}
		checked++
	}
	for p, m := range members {
		if m.typ != tar.TypeDir && !listed[p] {
			return 0, importErr("invalid_artifact", "%s is in the artifact but not in FILES.json.", p)
		}
	}
	return checked, nil
}

// memberSum is the checksum of the file a member is, or links to.
func memberSum(members map[string]*artifactMember, p string) (string, error) {
	for range maxLinkHops {
		m := members[p]
		switch {
		case m == nil:
			return "", importErr("invalid_artifact", "%s links to a file the artifact lacks.", p)
		case m.typ == tar.TypeReg:
			return m.sum, nil
		case m.typ != tar.TypeSymlink:
			return "", importErr("checksum_mismatch", "FILES.json lists %s as a file, which it isn't.", p)
		}
		p = m.link
	}
	return "", importErr("invalid_artifact", "%s: too many links.", p)
}

// checkCollectionInfo checks MANIFEST.json's collection_info as
// galaxy-importer does.
func checkCollectionInfo(a *CollectionArtifact, members map[string]*artifactMember, log *ImportLog) error {
	a.Namespace, a.Name, a.Version = str(a.Info["namespace"]), str(a.Info["name"]), str(a.Info["version"])
	bad := func(format string, args ...any) error { return importErr("invalid_metadata", format, args...) }
	switch {
	case !validGalaxyName(a.Namespace):
		return bad("Invalid namespace %q: lowercase letters, digits and single underscores, starting with a letter.", a.Namespace)
	case !validGalaxyName(a.Name):
		return bad("Invalid collection name %q: lowercase letters, digits and single underscores, starting with a letter.", a.Name)
	case !semverRe.MatchString(a.Version):
		return bad("Version %q is not a semantic version.", a.Version)
	}
	if list, ok := a.Info["authors"].([]any); !ok || len(list) == 0 || !allStrings(list) {
		return bad("collection_info's authors is not a list of names.")
	}
	readme, _ := a.Info["readme"].(string)
	if readme == "" {
		return bad("collection_info names no readme.")
	}
	if p, ok := memberPath(readme); !ok || members[p] == nil {
		return bad("The readme, %s, is not in the artifact.", readme)
	}
	if tags, ok := a.Info["tags"]; ok && tags != nil {
		list, ok := tags.([]any)
		if !ok {
			return bad("collection_info's tags is not a list.")
		}
		if len(list) > maxCollectionTags {
			return bad("The collection has %d tags, more than %d.", len(list), maxCollectionTags)
		}
		for _, t := range list {
			if s, ok := t.(string); !ok || !tagRe.MatchString(s) {
				return bad("Invalid tag %v: lowercase letters, digits and underscores.", t)
			}
		}
	}
	license, _ := a.Info["license"].([]any)
	if !allStrings(license) {
		return bad("collection_info's license is not a list of SPDX identifiers.")
	}
	if lf, _ := a.Info["license_file"].(string); len(license) == 0 && lf == "" {
		log.warn("collection_info gives no license or license_file.")
	}
	deps := map[string]any{}
	if v, ok := a.Info["dependencies"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			return bad("collection_info's dependencies is not a mapping.")
		}
		deps = m
	}
	a.Info["dependencies"] = deps
	for name, spec := range deps {
		ns, coll, _ := strings.Cut(name, ".")
		if !validGalaxyName(ns) || !validGalaxyName(coll) {
			return bad("Dependency %q is not a collection's full name (namespace.name).", name)
		}
		if name == a.Namespace+"."+a.Name {
			return bad("The collection depends on itself.")
		}
		s, ok := spec.(string)
		if !ok || !validVersionSpec(s) {
			return bad("Dependency %s: %v is not a version range such as >=1.0.0,<2.0.0.", name, spec)
		}
	}
	if a.RequiresAnsible == "" {
		log.warn("meta/runtime.yml gives no requires_ansible.")
	}
	needs := "no other collection"
	if len(deps) > 0 {
		var list []string
		for name, spec := range deps {
			list = append(list, name+" "+str(spec))
		}
		sort.Strings(list)
		needs = strings.Join(list, ", ")
	}
	log.info("Metadata: %s.%s %s, requires Ansible %s, depends on %s.", a.Namespace, a.Name, a.Version, orNone(a.RequiresAnsible), needs)
	return nil
}

func allStrings(list []any) bool {
	for _, v := range list {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

func orNone(s string) string {
	if s == "" {
		return "(not given)"
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// ImportCollection checks a collection tarball and stores it as a version
// of its collection, in its namespace, in a repository. The namespace must
// exist. It returns the version's id.
func ImportCollection(ctx context.Context, app *api.App, repository string, data []byte) (int64, error) {
	a, err := ReadCollectionArtifact(data)
	if err != nil {
		return 0, err
	}
	var nsID, repoID int64
	if err := app.DB.QueryRow(ctx, "SELECT id FROM collection_namespaces WHERE name = $1", a.Namespace).Scan(&nsID); err != nil {
		return 0, importErr("namespace_not_found", "Namespace %q does not exist.", a.Namespace)
	}
	if err := app.DB.QueryRow(ctx, "SELECT id FROM collection_repositories WHERE name = $1", repository).Scan(&repoID); err != nil {
		return 0, fmt.Errorf("no repository %s", repository)
	}
	if versionTaken(ctx, app, nsID, a) {
		return 0, versionExists(a)
	}
	return storeCollectionVersion(ctx, app, a, data, nsID, repoID, 0)
}

// versionTaken reports whether the namespace has the artifact's version,
// in any repository.
func versionTaken(ctx context.Context, app *api.App, nsID int64, a *CollectionArtifact) bool {
	var exists bool
	app.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		WHERE c.namespace_id = $1 AND c.name = $2 AND v.version = $3)`, nsID, a.Name, a.Version).Scan(&exists)
	return exists
}

// storeCollectionVersion stores a checked artifact as a new version, in
// the hub's storage, and adds it to a repository.
func storeCollectionVersion(ctx context.Context, app *api.App, a *CollectionArtifact, data []byte, nsID, repoID, by int64) (int64, error) {
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
	var repo string
	if err := app.DB.QueryRow(ctx, "SELECT name FROM collection_repositories WHERE id = $1", repoID).Scan(&repo); err != nil {
		return 0, err
	}
	if repo == hubPublished {
		if err := ensureSigningKey(ctx, app); err != nil {
			return 0, err
		}
	}
	var id int64
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		var collID int64
		if err := tx.QueryRow(ctx, `INSERT INTO collections (namespace_id, name) VALUES ($1, $2)
			ON CONFLICT (namespace_id, name) DO UPDATE SET modified = now() RETURNING id`, nsID, a.Name).Scan(&collID); err != nil {
			return err
		}
		info, _ := json.Marshal(a.Info)
		manifest, _ := json.Marshal(a.Manifest)
		files, _ := json.Marshal(a.Files)
		err := tx.QueryRow(ctx, `INSERT INTO collection_versions (collection_id, version, requires_ansible, metadata, manifest, files,
			filename, sha256, size, storage, location, created_by_id, manifest_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, nullif($12, 0), $13)
			ON CONFLICT (collection_id, version) DO NOTHING RETURNING id`,
			collID, a.Version, a.RequiresAnsible, info, manifest, files, filename, digest, int64(len(data)), kind, location, by, a.ManifestJSON).Scan(&id)
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
		if _, err := tx.Exec(ctx, "INSERT INTO collection_repository_contents (repository_id, version_id) VALUES ($1, $2)", repoID, id); err != nil {
			return err
		}
		if repo == hubPublished {
			return signPublished(ctx, app, tx, id, by)
		}
		return nil
	})
	return id, err
}

func versionExists(a *CollectionArtifact) error {
	return importErr("duplicate_version", "%s.%s %s already exists: a version is uploaded once. Upload a new version, or delete this one first.",
		a.Namespace, a.Name, a.Version)
}

// validVersionSpec reports whether s is a version range as ansible-galaxy
// reads them: *, or comparisons separated by commas.
func validVersionSpec(s string) bool {
	if strings.TrimSpace(s) == "*" {
		return true
	}
	for _, part := range strings.Split(s, ",") {
		if _, _, ok := parseVersionReq(part); !ok {
			return false
		}
	}
	return true
}

var specOps = []string{"!=", "==", ">=", "<=", ">", "<", "="}

// parseVersionReq splits a comparison such as >=1.2 into its operator and
// version, completing a loose version (1.2) to a semantic one.
func parseVersionReq(req string) (op, version string, ok bool) {
	req = strings.TrimSpace(req)
	op = "=="
	for _, o := range specOps {
		if strings.HasPrefix(req, o) {
			op, req = o, strings.TrimSpace(req[len(o):])
			break
		}
	}
	if op == "=" {
		op = "=="
	}
	if req == "*" {
		return op, req, true
	}
	if !semverRe.MatchString(req) {
		parts := strings.Split(req, ".")
		if len(parts) > 3 {
			return "", "", false
		}
		for _, p := range parts {
			if _, err := strconv.ParseUint(p, 10, 64); err != nil {
				return "", "", false
			}
		}
		for len(parts) < 3 {
			parts = append(parts, "0")
		}
		req = strings.Join(parts, ".")
		if !semverRe.MatchString(req) {
			return "", "", false
		}
	}
	return op, req, true
}

// versionMeets reports whether version is in the range spec, as
// ansible-galaxy's meets_requirements decides; an empty spec is any.
func versionMeets(version, spec string) bool {
	if s := strings.TrimSpace(spec); s == "" || s == "*" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		op, req, ok := parseVersionReq(part)
		if !ok {
			return false
		}
		if req == "*" {
			continue
		}
		c := compareSemver(version, req)
		var meets bool
		switch op {
		case "==":
			meets = c == 0
		case "!=":
			meets = c != 0
		case ">=":
			meets = c >= 0
		case "<=":
			meets = c <= 0
		case ">":
			meets = c > 0
		case "<":
			meets = c < 0
		}
		if !meets {
			return false
		}
	}
	return true
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

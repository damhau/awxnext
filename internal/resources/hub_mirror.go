package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/hubsign"
	"github.com/damhau/awxnext/internal/rbac"
)

// maxSignatureFileBytes bounds a file of signatures beside an artifact.
const maxSignatureFileBytes = 1 << 20

// hubMirror adds collections made elsewhere, mirrored from a remote or
// imported from a directory, to one repository.
type hubMirror struct {
	app    *api.App
	repoID int64
	repo   string
	// org owns the namespaces the mirror creates; with none, it creates none.
	org    int64
	origin string
	// keys are the public keys signatures must verify with, keysName where
	// they are from. Without keys, signatures are kept unchecked.
	keys, keysName string
	by             int64
}

// hubVersionRef is a version the hub has, in its repositories.
type hubVersionRef struct {
	id     int64
	sha256 string
	repos  []string
}

type mirroredSignature struct {
	signature, fingerprint, signer, publicKey string
}

type mirrorOutcome struct {
	// stored: the hub didn't have the version; added: it entered the
	// repository.
	stored, added bool
	signatures    int
	skipped       string
	warnings      []string
}

func (o mirrorOutcome) describe() string {
	s := "already in the repository"
	switch {
	case o.stored:
		s = "added"
	case o.added:
		s = "added, from the hub's copy"
	}
	switch {
	case o.signatures == 1:
		s += ", 1 signature added"
	case o.signatures > 1:
		s += fmt.Sprintf(", %d signatures added", o.signatures)
	}
	return s
}

// existing is the hub's version of a collection, in any repository.
func (m *hubMirror) existing(ctx context.Context, ns, name, version string) (*hubVersionRef, error) {
	v := &hubVersionRef{}
	err := m.app.DB.QueryRow(ctx, `SELECT v.id, v.sha256, coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), '{}')
		FROM collection_versions v JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id
		LEFT JOIN collection_repository_contents rc ON rc.version_id = v.id LEFT JOIN collection_repositories r ON r.id = rc.repository_id
		WHERE t.name = $1 AND c.name = $2 AND v.version = $3 GROUP BY v.id`, ns, name, version).Scan(&v.id, &v.sha256, &v.repos)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// versionsHeld lists the versions of a collection in the repository.
func (m *hubMirror) versionsHeld(ctx context.Context, ns, name string) ([]string, error) {
	rows, err := m.app.DB.Query(ctx, `SELECT v.version FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id JOIN collection_repository_contents rc ON rc.version_id = v.id
		WHERE rc.repository_id = $1 AND t.name = $2 AND c.name = $3`, m.repoID, ns, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// checkSignatures keeps the signatures of a MANIFEST.json the mirror may
// serve: those its keys verify, or with no keys, those ansible-galaxy can
// read.
func (m *hubMirror) checkSignatures(manifest []byte, sigs []string) ([]mirroredSignature, []string) {
	var out []mirroredSignature
	var warnings []string
	seen := map[string]bool{}
	for i, sig := range sigs {
		var s mirroredSignature
		if m.keys != "" {
			armored, signer, err := hubsign.Verify(m.keys, manifest, sig)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("signature %d dropped: %v", i+1, err))
				continue
			}
			s = mirroredSignature{armored, signer.Fingerprint, signer.UserID, signer.PublicKey}
		} else {
			armored, fp, err := hubsign.Inspect(sig)
			if err == nil && fp == "" {
				err = errors.New("it names no key")
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("signature %d dropped: %v", i+1, err))
				continue
			}
			s = mirroredSignature{signature: armored, fingerprint: fp}
		}
		if !seen[s.fingerprint] {
			seen[s.fingerprint] = true
			out = append(out, s)
		}
	}
	return out, warnings
}

func (m *hubMirror) unsigned() string {
	return "no signature verifies with the keys of " + m.keysName
}

// addExisting adds a version the hub has to the repository, with the
// signatures it lacks.
func (m *hubMirror) addExisting(ctx context.Context, v *hubVersionRef, sigs []string) (mirrorOutcome, error) {
	var out mirrorOutcome
	in := false
	for _, r := range v.repos {
		switch {
		case r == m.repo:
			in = true
		case r == hubStaging:
			return out, errors.New("the hub has it in staging, waiting for approval: approve or reject it first")
		case r == hubRejected:
			return out, errors.New("its namespace's approvers rejected it: delete it from rejected first")
		}
	}
	if !in && curatedOnly(m.repo) && len(v.repos) > 0 {
		return out, fmt.Errorf("the hub has it in %s already", strings.Join(v.repos, ", "))
	}
	manifest, err := versionManifest(ctx, m.app, m.app.DB, v.id)
	if err != nil {
		return out, err
	}
	keep, warnings := m.checkSignatures(manifest, sigs)
	out.warnings = warnings
	if m.keys != "" && len(keep) == 0 && !in {
		out.skipped = m.unsigned()
		return out, nil
	}
	if !in && m.repo == hubPublished {
		if err := ensureSigningKey(ctx, m.app); err != nil {
			return out, err
		}
	}
	err = pgx.BeginFunc(ctx, m.app.DB, func(tx pgx.Tx) error {
		if !in {
			tag, err := tx.Exec(ctx, "INSERT INTO collection_repository_contents (repository_id, version_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", m.repoID, v.id)
			if err != nil {
				return err
			}
			out.added = tag.RowsAffected() > 0
			if out.added && m.repo == hubPublished {
				if err := signPublished(ctx, m.app, tx, v.id, m.by); err != nil {
					return err
				}
			}
		}
		if out.signatures, err = m.addSignatures(ctx, tx, v.id, keep); err != nil {
			return err
		}
		if out.added || out.signatures > 0 {
			_, err = tx.Exec(ctx, `UPDATE collections SET modified = now() WHERE id = (SELECT collection_id FROM collection_versions WHERE id = $1)`, v.id)
		}
		return err
	})
	return out, err
}

// addArtifact checks an artifact and adds it to the repository with its
// signatures, storing it when the hub doesn't have it. The artifact must
// be the version expect names, in its non-empty fields.
func (m *hubMirror) addArtifact(ctx context.Context, data []byte, sigs []string, expect *CollectionArtifact) (mirrorOutcome, error) {
	var out mirrorOutcome
	a, err := checkCollectionArtifact(data, &ImportLog{})
	if err != nil {
		return out, err
	}
	if expect != nil && (expect.Namespace != "" && expect.Namespace != a.Namespace || expect.Name != "" && expect.Name != a.Name ||
		expect.Version != "" && expect.Version != a.Version) {
		return out, fmt.Errorf("the artifact holds %s.%s %s", a.Namespace, a.Name, a.Version)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	for attempt := 0; ; attempt++ {
		have, err := m.existing(ctx, a.Namespace, a.Name, a.Version)
		if err != nil {
			return out, err
		}
		if have != nil {
			if have.sha256 != digest {
				return out, fmt.Errorf("the hub has another artifact of %s.%s %s (SHA-256 %s, this one's %s), which it keeps: versions don't change",
					a.Namespace, a.Name, a.Version, have.sha256, digest)
			}
			return m.addExisting(ctx, have, sigs)
		}
		keep, warnings := m.checkSignatures(a.ManifestJSON, sigs)
		out.warnings = warnings
		if m.keys != "" && len(keep) == 0 {
			out.skipped = m.unsigned()
			return out, nil
		}
		nsID, err := m.namespace(ctx, a.Namespace)
		if err != nil {
			return out, err
		}
		id, err := storeCollectionVersion(ctx, m.app, a, data, nsID, m.repoID, m.by)
		var ie *ImportError
		if errors.As(err, &ie) && ie.Code == "duplicate_version" && attempt == 0 {
			// Stored at the same time by another sync.
			continue
		}
		if err != nil {
			return out, err
		}
		out.stored, out.added = true, true
		kickDocs()
		out.signatures, err = m.addSignatures(ctx, m.app.DB, id, keep)
		return out, err
	}
}

// heldVersion is a version in the mirror's repository; shared when another
// repository holds it too.
type heldVersion struct {
	id     int64
	key    string
	shared bool
}

// heldVersions lists the versions in the mirror's repository.
func (m *hubMirror) heldVersions(ctx context.Context) ([]heldVersion, error) {
	rows, err := m.app.DB.Query(ctx, `SELECT v.id, t.name || '.' || c.name || ' ' || v.version,
		EXISTS (SELECT 1 FROM collection_repository_contents o WHERE o.version_id = v.id AND o.repository_id <> rc.repository_id)
		FROM collection_repository_contents rc JOIN collection_versions v ON v.id = rc.version_id
		JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id
		WHERE rc.repository_id = $1 ORDER BY t.name, c.name, v.id`, m.repoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (heldVersion, error) {
		var h heldVersion
		return h, row.Scan(&h.id, &h.key, &h.shared)
	})
}

// recheckVersion checks the signatures of a version in the repository
// with the keys. It takes the version out of the repository when none
// verifies, and drops those that don't. A shared version keeps them all:
// a signature these keys don't verify may be the one another repository's
// clients check, awxnext's own in published for one.
func (m *hubMirror) recheckVersion(ctx context.Context, v heldVersion) (kept bool, dropped int, err error) {
	manifest, err := versionManifest(ctx, m.app, m.app.DB, v.id)
	if err != nil {
		return false, 0, err
	}
	rows, err := m.app.DB.Query(ctx, "SELECT id, signature FROM collection_version_signatures WHERE collection_version_id = $1", v.id)
	if err != nil {
		return false, 0, err
	}
	var bad []int64
	for rows.Next() {
		var id int64
		var sig string
		if err := rows.Scan(&id, &sig); err != nil {
			rows.Close()
			return false, 0, err
		}
		if _, _, err := hubsign.Verify(m.keys, manifest, sig); err != nil {
			bad = append(bad, id)
		} else {
			kept = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, 0, err
	}
	if !kept {
		_, err = m.app.DB.Exec(ctx, "DELETE FROM collection_repository_contents WHERE repository_id = $1 AND version_id = $2", m.repoID, v.id)
		return false, 0, err
	}
	if len(bad) == 0 || v.shared {
		return true, 0, nil
	}
	err = pgx.BeginFunc(ctx, m.app.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM collection_version_signatures WHERE id = ANY($1)", bad)
		if err != nil {
			return err
		}
		dropped = int(tag.RowsAffected())
		_, err = tx.Exec(ctx, `UPDATE collections SET modified = now() WHERE id = (SELECT collection_id FROM collection_versions WHERE id = $1)`, v.id)
		return err
	})
	return true, dropped, err
}

// deleteUnheldVersions deletes those of the versions no repository holds,
// with their artifacts, and the collections left with none. The
// versions' collections are marked modified.
func deleteUnheldVersions(ctx context.Context, db api.DBTX, versions []int64) error {
	rows, err := db.Query(ctx, `UPDATE collections SET modified = now() WHERE id IN (SELECT collection_id FROM collection_versions WHERE id = ANY($1))
		RETURNING id`, versions)
	if err != nil {
		return err
	}
	colls, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM collection_versions v WHERE v.id = ANY($1)
		AND NOT EXISTS (SELECT 1 FROM collection_repository_contents rc WHERE rc.version_id = v.id)`, versions); err != nil {
		return err
	}
	_, err = db.Exec(ctx, `DELETE FROM collections c WHERE c.id = ANY($1)
		AND NOT EXISTS (SELECT 1 FROM collection_versions v WHERE v.collection_id = c.id)`, colls)
	return err
}

func (m *hubMirror) addSignatures(ctx context.Context, db api.DBTX, versionID int64, sigs []mirroredSignature) (int, error) {
	n := 0
	for _, s := range sigs {
		tag, err := db.Exec(ctx, `INSERT INTO collection_version_signatures (created_by_id, collection_version_id, origin, signature, pubkey_fingerprint, signer, public_key)
			VALUES (nullif($1, 0), $2, $3, $4, $5, $6, $7) ON CONFLICT (collection_version_id, pubkey_fingerprint) DO NOTHING`,
			m.by, versionID, m.origin, s.signature, s.fingerprint, s.signer, s.publicKey)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// namespace is the hub's namespace of that name, created in the mirror's
// organization when it has none.
func (m *hubMirror) namespace(ctx context.Context, name string) (int64, error) {
	var id int64
	err := m.app.DB.QueryRow(ctx, "SELECT id FROM collection_namespaces WHERE name = $1", name).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if m.org == 0 {
		return 0, importErr("namespace_not_found", "The hub has no namespace %s: give an organization to create it in.", name)
	}
	err = pgx.BeginFunc(ctx, m.app.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO collection_namespaces (name, organization_id, description) VALUES ($1, $2, $3)
			ON CONFLICT (name) DO NOTHING RETURNING id`, name, m.org, "Created by a mirror into the repository "+m.repo+".").Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, "SELECT id FROM collection_namespaces WHERE name = $1", name).Scan(&id)
		}
		if err != nil {
			return err
		}
		return rbac.EnsureObjectRoles(ctx, tx, "collectionnamespace", id)
	})
	return id, err
}

// CollectionDirectory is a directory of collection artifacts, as
// ansible-galaxy collection download writes it, to import into a
// repository: awxnext hub import.
type CollectionDirectory struct {
	Dir string
	// Repository is published when empty.
	Repository string
	// Organization owns the namespaces the import creates.
	Organization string
	// Keys are public keys, armored or binary, the signatures must verify
	// with: then a collection with none that does is not imported.
	Keys string
}

// directoryEntry is an artifact of the directory, with its signature files.
type directoryEntry struct {
	file, version string
	signatures    []string
}

// ImportCollectionDirectory imports the artifacts of a directory, with
// their signatures: those its requirements.yml lists, and each artifact's
// .asc file.
func ImportCollectionDirectory(ctx context.Context, app *api.App, out io.Writer, d CollectionDirectory) error {
	root, err := os.OpenRoot(d.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	m := &hubMirror{app: app, repo: d.Repository, origin: originImport, keys: d.Keys, keysName: "the keyring"}
	if m.repo == "" {
		m.repo = hubPublished
	}
	if err := app.DB.QueryRow(ctx, "SELECT id FROM collection_repositories WHERE name = $1", m.repo).Scan(&m.repoID); err != nil {
		return fmt.Errorf("the hub has no repository %s", m.repo)
	}
	if m.repo == hubRejected {
		return errors.New("collections are imported into a repository clients install from, or into staging, not into rejected")
	}
	if d.Organization != "" {
		if err := app.DB.QueryRow(ctx, "SELECT id FROM organizations WHERE name = $1", d.Organization).Scan(&m.org); err != nil {
			return fmt.Errorf("no organization %s", d.Organization)
		}
	}
	if d.Keys != "" {
		if _, err := hubsign.ReadKeys(d.Keys); err != nil {
			return fmt.Errorf("the keyring: %w", err)
		}
	}
	entries, err := directoryEntries(root)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s has no collection artifact (.tar.gz)", d.Dir)
	}
	limit := min(int64(app.Settings.Int(ctx, "HUB_MAX_UPLOAD_SIZE")), maxUploadLimit)
	var imported, present, skipped, failed int
	for _, e := range entries {
		outcome, err := importEntry(ctx, m, root, e, limit)
		for _, w := range outcome.warnings {
			fmt.Fprintf(out, "%s: WARNING: %s\n", e.file, w)
		}
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(out, "%s: ERROR: %v\n", e.file, err)
		case outcome.skipped != "":
			skipped++
			fmt.Fprintf(out, "%s: skipped, %s\n", e.file, outcome.skipped)
		default:
			if outcome.added {
				imported++
			} else {
				present++
			}
			fmt.Fprintf(out, "%s: %s\n", e.file, outcome.describe())
		}
	}
	fmt.Fprintf(out, "Imported into %s: %d added, %d already there, %d skipped, %d failed.\n", m.repo, imported, present, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d collections failed to import", failed, len(entries))
	}
	return nil
}

func importEntry(ctx context.Context, m *hubMirror, root *os.Root, e directoryEntry, limit int64) (mirrorOutcome, error) {
	data, err := readDirFile(root, e.file, limit)
	if err != nil {
		return mirrorOutcome{}, err
	}
	var sigs []string
	for _, f := range e.signatures {
		text, err := readDirFile(root, f, maxSignatureFileBytes)
		if err != nil {
			return mirrorOutcome{}, fmt.Errorf("its signatures: %w", err)
		}
		sigs = append(sigs, hubsign.Signatures(string(text))...)
	}
	return m.addArtifact(ctx, data, sigs, &CollectionArtifact{Version: e.version})
}

// directoryEntries lists the directory's artifacts: those its
// requirements.yml lists, or every .tar.gz without one.
func directoryEntries(root *os.Root) ([]directoryEntry, error) {
	var entries []directoryEntry
	raw, err := readDirFile(root, "requirements.yml", maxManifestBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		dir, err := root.Open(".")
		if err != nil {
			return nil, err
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return nil, err
		}
		sort.Strings(names)
		for _, n := range names {
			if strings.HasSuffix(n, ".tar.gz") {
				entries = append(entries, directoryEntry{file: n})
			}
		}
	case err != nil:
		return nil, err
	default:
		var doc struct {
			Collections []struct {
				Name       string   `yaml:"name"`
				Version    string   `yaml:"version"`
				Signatures []string `yaml:"signatures"`
			} `yaml:"collections"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("requirements.yml: %w", err)
		}
		for i, c := range doc.Collections {
			if !plainFileName(c.Name) || !strings.HasSuffix(c.Name, ".tar.gz") {
				return nil, fmt.Errorf("requirements.yml, collection %d: %q is not the file name of an artifact in the directory, as ansible-galaxy collection download writes it", i+1, c.Name)
			}
			e := directoryEntry{file: c.Name, version: c.Version}
			for _, s := range c.Signatures {
				p := strings.TrimPrefix(s, "file://")
				if p == s && strings.Contains(s, "://") {
					return nil, fmt.Errorf("requirements.yml, %s: the signature %s is not a file of the directory", c.Name, s)
				}
				e.signatures = append(e.signatures, p)
			}
			entries = append(entries, e)
		}
	}
	for i := range entries {
		asc := entries[i].file + ".asc"
		if _, err := root.Stat(asc); err == nil {
			entries[i].signatures = append(entries[i].signatures, asc)
		}
	}
	return entries, nil
}

func plainFileName(n string) bool {
	return n != "" && n != "." && n != ".." && path.Base(n) == n && !strings.ContainsAny(n, `/\`)
}

// readDirFile reads a file of the directory, at most limit bytes.
func readDirFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %s", name, humanBytes(limit))
	}
	return data, nil
}

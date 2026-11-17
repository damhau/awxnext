package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/hubsign"
)

// Signing the hub's collections (roadmap M13-03): approval signs a
// version's MANIFEST.json with awxnext's active key, approvers upload
// signatures made elsewhere, and the v3 API serves them all, as
// ansible-galaxy's --keyring verifies them.

const (
	defaultSigningKeyName = "awxnext collection hub"
	// galaxy_ng's default signing service: its name in the signatures the
	// hub's key makes.
	hubSigningService = "ansible-default"
	originAwxnext     = "awxnext"
	originUpload      = "upload"
	// The most valid signatures a project may require of each collection.
	maxSignatureCount = 10
)

// ensureSigningKey makes awxnext's first signing key when it has none, in a
// transaction of its own: the transaction that signs only reads it.
func ensureSigningKey(ctx context.Context, app *api.App) error {
	var exists bool
	if err := app.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM collection_signing_keys WHERE active)").Scan(&exists); err != nil || exists {
		return err
	}
	k, err := hubsign.Generate(defaultSigningKeyName, "")
	if err != nil {
		return fmt.Errorf("generating the hub's signing key: %w", err)
	}
	private, err := app.Crypto.Encrypt(k.PrivateKey)
	if err != nil {
		return err
	}
	_, err = app.DB.Exec(ctx, `INSERT INTO collection_signing_keys (name, description, fingerprint, algorithm, public_key, private_key, active)
		VALUES ($1, $2, $3, $4, $5, $6, true) ON CONFLICT (active) WHERE active DO NOTHING`,
		defaultSigningKeyName, "Made by awxnext for its first signature.", k.Fingerprint, k.Algorithm, k.PublicKey, private)
	return err
}

// hubSigner is awxnext's active signing key, ready to sign.
type hubSigner struct {
	app         *api.App
	id          int64
	fingerprint string
	userID      string
	private     string
}

func loadHubSigner(ctx context.Context, app *api.App, db api.DBTX) (*hubSigner, error) {
	s := &hubSigner{app: app}
	var name, email, private string
	err := db.QueryRow(ctx, "SELECT id, fingerprint, name, email, private_key FROM collection_signing_keys WHERE active").
		Scan(&s.id, &s.fingerprint, &name, &email, &private)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("the hub has no active signing key")
	}
	if err != nil {
		return nil, err
	}
	if s.private, err = app.Crypto.Decrypt(private); err != nil {
		return nil, fmt.Errorf("unwrapping the hub's signing key: %w", err)
	}
	s.userID = keyUserID(name, email)
	return s, nil
}

func keyUserID(name, email string) string {
	if email == "" {
		return name
	}
	return name + " <" + email + ">"
}

// sign signs a version's MANIFEST.json, unless the key already did. by
// is the user whose approval signs it.
func (s *hubSigner) sign(ctx context.Context, db api.DBTX, versionID, by int64) error {
	var done bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collection_version_signatures WHERE collection_version_id = $1 AND pubkey_fingerprint = $2)`,
		versionID, s.fingerprint).Scan(&done); err != nil || done {
		return err
	}
	manifest, err := versionManifest(ctx, s.app, db, versionID)
	if err != nil {
		return err
	}
	sig, err := hubsign.Sign(s.private, manifest)
	if err != nil {
		return fmt.Errorf("signing: %w", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO collection_version_signatures (created_by_id, collection_version_id, origin, signature, pubkey_fingerprint, signer, signing_key_id)
		VALUES (nullif($1, 0), $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, by, versionID, originAwxnext, sig, s.fingerprint, s.userID, s.id)
	return err
}

// signPublished signs a version that entered the published repository.
func signPublished(ctx context.Context, app *api.App, db api.DBTX, versionID, by int64) error {
	s, err := loadHubSigner(ctx, app, db)
	if err != nil {
		return err
	}
	return s.sign(ctx, db, versionID, by)
}

// versionManifest is a version's MANIFEST.json as its artifact holds it,
// read from the artifact for versions stored before it was kept.
func versionManifest(ctx context.Context, app *api.App, db api.DBTX, versionID int64) ([]byte, error) {
	var manifest []byte
	var storage, location string
	if err := db.QueryRow(ctx, "SELECT manifest_json, storage, location FROM collection_versions WHERE id = $1", versionID).
		Scan(&manifest, &storage, &location); err != nil {
		return nil, err
	}
	if manifest != nil {
		return manifest, nil
	}
	data, err := loadArtifact(ctx, app, db, versionID, storage, location)
	if err != nil {
		return nil, err
	}
	a, err := ReadCollectionArtifact(data)
	if err != nil {
		return nil, fmt.Errorf("reading the artifact's MANIFEST.json: %w", err)
	}
	if _, err := db.Exec(ctx, "UPDATE collection_versions SET manifest_json = $2 WHERE id = $1", versionID, a.ManifestJSON); err != nil {
		return nil, err
	}
	return a.ManifestJSON, nil
}

// loadArtifact reads a version's artifact from where it is stored;
// pgx.ErrNoRows when the database has none.
func loadArtifact(ctx context.Context, app *api.App, db api.DBTX, versionID int64, storage, location string) ([]byte, error) {
	if storage == "database" {
		var data []byte
		err := db.QueryRow(ctx, "SELECT data FROM collection_version_data WHERE version_id = $1", versionID).Scan(&data)
		return data, err
	}
	store, err := hubStore(app)(ctx, storage)
	if err != nil {
		return nil, err
	}
	return store.Get(ctx, location)
}

// versionSignatures are a version's signatures in galaxy_ng's shape.
func versionSignatures(ctx context.Context, db api.DBTX, versionID int64) ([]Obj, error) {
	rows, err := db.Query(ctx, `SELECT signature, pubkey_fingerprint, origin, created FROM collection_version_signatures
		WHERE collection_version_id = $1 ORDER BY id`, versionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Obj, error) {
		var sig, fp, origin string
		var created time.Time
		if err := row.Scan(&sig, &fp, &origin, &created); err != nil {
			return nil, err
		}
		var service any
		if origin == originAwxnext {
			service = hubSigningService
		}
		return Obj{"signature": sig, "pubkey_fingerprint": fp, "signing_service": service, "pulp_created": galaxyTime(created)}, nil
	})
}

// signatureSummaries are a version's signatures in its summary fields:
// who signed, with which key, and who had it signed.
func signatureSummaries(c *Ctx, versionID int64) ([]Obj, error) {
	rows, err := c.DB().Query(c, `SELECT s.id, s.pubkey_fingerprint, s.signer, s.origin, s.signing_key_id, coalesce(u.username, ''), s.created
		FROM collection_version_signatures s LEFT JOIN users u ON u.id = s.created_by_id WHERE s.collection_version_id = $1 ORDER BY s.id`, versionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Obj, error) {
		var id int64
		var fp, signer, origin, by string
		var key *int64
		var created time.Time
		if err := row.Scan(&id, &fp, &signer, &origin, &key, &by, &created); err != nil {
			return nil, err
		}
		return Obj{"id": id, "pubkey_fingerprint": fp, "signer": signer, "origin": origin, "signing_key": key,
			"created_by": by, "created": api.FormatTime(created)}, nil
	})
}

// HubKeyring is the public keys of awxnext's signing keys, the active one
// first: those whose signatures the hub serves.
func HubKeyring(ctx context.Context, app *api.App) ([]string, error) {
	rows, err := app.DB.Query(ctx, "SELECT public_key FROM collection_signing_keys ORDER BY active DESC, id DESC")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func installHubKeyring(app *api.App) {
	serve := func(armored bool) func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		return func(c *Ctx, w http.ResponseWriter, r *http.Request) {
			keys, err := HubKeyring(c, c.App)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			if len(keys) == 0 {
				galaxyError(w, 404, "not_found", "The hub has no signing key yet: it makes one when it first signs a collection.")
				return
			}
			var body []byte
			if armored {
				s, err := hubsign.ArmoredKeyring(keys...)
				if err != nil {
					api.WriteError(w, err)
					return
				}
				body = []byte(s)
				w.Header().Set("Content-Type", "application/pgp-keys")
			} else {
				if body, err = hubsign.Keyring(keys...); err != nil {
					api.WriteError(w, err)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(200)
			w.Write(body)
		}
	}
	// The trailing slash is the API's: /api/galaxy/keyring.asc reaches it.
	app.HandleAnonymous("GET", galaxyRoot+"keyring.asc/", serve(true))
	app.HandleAnonymous("GET", galaxyRoot+"keyring.gpg/", serve(false))
}

func requireSuperuser(c *Ctx) error {
	if !c.User.Superuser {
		return api.ErrForbidden
	}
	return nil
}

func registerCollectionSigning() {
	keys := api.Register(&Resource{
		Name:          "collection_signing_keys",
		Type:          "collection_signing_key",
		Table:         "collection_signing_keys",
		Verbose:       "Collection Signing Key",
		Ordering:      "-id",
		SearchFields:  []string{"name", "fingerprint"},
		SummaryFields: []string{"id", "name", "fingerprint", "active"},
		Fields: []*Field{
			{Name: "name", Type: api.TString, Default: defaultSigningKeyName, MaxLength: 256, CreateOnly: true,
				Help: "The name in the key's user ID, which gpg shows for a good signature."},
			{Name: "email", Type: api.TString, Default: "", MaxLength: 256, CreateOnly: true, Help: "The email of the key's user ID, if any."},
			{Name: "fingerprint", Type: api.TString, ReadOnly: true},
			{Name: "algorithm", Type: api.TString, ReadOnly: true},
			{Name: "public_key", Type: api.TText, ReadOnly: true, NoFilter: true, Help: "The armored public key, which verifies the key's signatures."},
			{Name: "private_key", Type: api.TSecret, ReadOnly: true, Hidden: true, NoFilter: true},
			{Name: "active", Type: api.TBool, ReadOnly: true, Help: "The key signs approvals; one key is active, the newest."},
			{Name: "retired", Type: api.TDateTime, ReadOnly: true, Nullable: true,
				Help: "When a newer key replaced it. Its signatures stay valid until the key is deleted."},
			{Name: "sign_published", Type: api.TBool, Virtual: true, WriteOnly: true, Default: true,
				Help: "Sign every published version with the new key at once, beside the older keys' signatures."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Anonymous() {
				return "FALSE"
			}
			return "TRUE"
		},
		CanAdd:    func(c *Ctx, _ Obj) error { return requireSuperuser(c) },
		CanChange: func(c *Ctx, _, _ Obj) error { return requireSuperuser(c) },
		CanDelete: func(c *Ctx, obj Obj) error {
			if err := requireSuperuser(c); err != nil {
				return err
			}
			if obj["active"] == true {
				return api.BadRequest("The active key signs approvals: create a new key first, then delete this one.")
			}
			return nil
		},
		Perm: func(c *Ctx, _ Obj, perm string) (bool, error) { return perm == "view" || c.User.Superuser, nil },
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			return map[string]bool{"edit": c.User.Superuser, "delete": c.User.Superuser && obj["active"] != true}
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if old != nil {
				return nil
			}
			k, err := hubsign.Generate(str(data["name"]), str(data["email"]))
			if err != nil {
				return api.FieldError("name", "The key can't be made with this user ID: "+err.Error())
			}
			if _, err := c.DB().Exec(c, "UPDATE collection_signing_keys SET active = false, retired = now(), modified = now() WHERE active"); err != nil {
				return err
			}
			data["fingerprint"], data["algorithm"], data["public_key"], data["private_key"], data["active"] =
				k.Fingerprint, k.Algorithm, k.PublicKey, k.PrivateKey, true
			c.Extra["sign_published"] = data["sign_published"] != false
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			if c.Extra["sign_published"] != true {
				return nil
			}
			return signAllPublished(c)
		},
	})

	api.Register(&Resource{
		Name:          "collection_version_signatures",
		Type:          "collection_version_signature",
		Table:         "collection_version_signatures",
		Verbose:       "Collection Version Signature",
		Ordering:      "id",
		NoName:        true,
		NoTimestamps:  true,
		NoUpdate:      true,
		SearchFields:  []string{"pubkey_fingerprint", "signer"},
		SummaryFields: []string{"id", "pubkey_fingerprint", "signer", "origin"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "created_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true,
				Help: "Who uploaded the signature, or whose approval had awxnext sign the version."},
			{Name: "collection_version", Type: api.TFK, Ref: "collection_versions", Required: true, CreateOnly: true},
			{Name: "origin", Type: api.TChoice, ReadOnly: true, Choices: []api.Choice{{Value: originAwxnext, Label: "Signed by awxnext"},
				{Value: originUpload, Label: "Uploaded"}, {Value: originRemote, Label: "Mirrored from a remote"},
				{Value: originImport, Label: "Imported from a directory"}}},
			{Name: "signature", Type: api.TText, Required: true, CreateOnly: true, NoFilter: true,
				Help: "The armored detached signature of the version's MANIFEST.json (gpg --armor --detach-sign MANIFEST.json)."},
			{Name: "public_key", Type: api.TText, Default: "", CreateOnly: true, NoFilter: true,
				Help: "The signer's armored public key, which must verify the uploaded signature."},
			{Name: "pubkey_fingerprint", Type: api.TString, ReadOnly: true, Help: "The fingerprint of the key that made the signature."},
			{Name: "signer", Type: api.TString, ReadOnly: true, Help: "The user ID of the key that made the signature."},
			{Name: "signing_key", Type: api.TFK, Ref: "collection_signing_keys", ReadOnly: true, Nullable: true,
				Help: "awxnext's key that made the signature; null for an uploaded one."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.collection_version_id IN (SELECT t.id FROM collection_versions t WHERE " +
				api.ViewPredicate(c, api.Lookup("collection_versions"), arg) + ")"
		},
		CanAdd: func(c *Ctx, data Obj) error {
			vid, _ := api.ID(data["collection_version"])
			ok, err := api.CanView(c, api.Lookup("collection_versions"), vid)
			if err != nil {
				return err
			}
			if !ok {
				return api.FieldError("collection_version", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", vid))
			}
			return requireVersionPerm(c, vid, "approve")
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			vid, _ := api.ID(obj["collection_version"])
			return requireVersionPerm(c, vid, "approve")
		},
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			if perm == "view" {
				return true, nil
			}
			vid, _ := api.ID(obj["collection_version"])
			return namespacePerm(c, namespaceOfVersion(c, vid), "approve")
		},
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			vid, _ := api.ID(obj["collection_version"])
			ok, _ := namespacePerm(c, namespaceOfVersion(c, vid), "approve")
			return map[string]bool{"delete": ok}
		},
		BeforeSave: uploadSignature,
		AfterDelete: func(c *Ctx, obj Obj) error {
			vid, _ := api.ID(obj["collection_version"])
			return touchVersion(c, vid)
		},
		ActivitySummary: signatureActivitySummary,
	})

	versions := api.Lookup("collection_versions")
	versions.SubLists = append(versions.SubLists, &SubList{
		Name: "signatures", Target: "collection_version_signatures", ParentField: "collection_version",
		Filter: func(arg ArgFunc, pid int64) string { return "t.collection_version_id = " + arg(pid) },
	})
	versions.Actions = append(versions.Actions, &api.Action{Name: "sign", Methods: []string{"POST"}, Perm: "approve",
		Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			if versionRepository(c, id) != hubPublished {
				api.WriteError(w, api.BadRequest("The version isn't published: approving it signs it."))
				return
			}
			if err := ensureSigningKey(c, c.App); err != nil {
				api.WriteError(w, err)
				return
			}
			err := c.WithTx(func(c *Ctx) error { return signPublished(c, c.App, c.DB(), id, c.User.UserID) })
			if err != nil {
				api.WriteError(w, err)
				return
			}
			fresh, err := api.Get(c, versions, id)
			if err == nil {
				fresh, err = api.SerializeOne(c, versions, fresh)
			}
			if err != nil {
				api.WriteError(w, err)
				return
			}
			api.WriteJSON(w, 200, fresh)
		}})
	keys.SubLists = append(keys.SubLists, &SubList{
		Name: "signatures", Target: "collection_version_signatures", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string { return "t.signing_key_id = " + arg(pid) },
	})
}

func namespaceOfVersion(c *Ctx, versionID int64) int64 {
	var ns int64
	c.DB().QueryRow(c, "SELECT c.namespace_id FROM collection_versions v JOIN collections c ON c.id = v.collection_id WHERE v.id = $1", versionID).Scan(&ns)
	return ns
}

func requireVersionPerm(c *Ctx, versionID int64, perm string) error {
	return requireNamespacePerm(c, namespaceOfVersion(c, versionID), perm)
}

// uploadSignature checks a signature made elsewhere against the signer's
// public key and the version's MANIFEST.json, and records who made it.
func uploadSignature(c *Ctx, old, data Obj) error {
	if old != nil {
		return nil
	}
	vid, _ := api.ID(data["collection_version"])
	if str(data["public_key"]) == "" {
		return api.FieldError("public_key", "Give the signer's armored public key: the hub checks the signature with it.")
	}
	manifest, err := versionManifest(c, c.App, c.DB(), vid)
	if err != nil {
		return err
	}
	sig, signer, err := hubsign.Verify(str(data["public_key"]), manifest, str(data["signature"]))
	if err != nil {
		return api.FieldError("signature", "The signature of the version's MANIFEST.json can't be accepted: "+err.Error()+".")
	}
	var taken bool
	c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM collection_version_signatures WHERE collection_version_id = $1 AND pubkey_fingerprint = $2)",
		vid, signer.Fingerprint).Scan(&taken)
	if taken {
		return api.FieldError("signature", fmt.Sprintf("The version already has a signature by key %s.", signer.Fingerprint))
	}
	data["signature"], data["public_key"] = sig, signer.PublicKey
	data["origin"], data["pubkey_fingerprint"], data["signer"] = originUpload, signer.Fingerprint, signer.UserID
	if !c.User.Anonymous() {
		data["created_by"] = c.User.UserID
	}
	return touchVersion(c, vid)
}

// touchVersion marks a version's collection changed: what the hub serves
// of it did.
func touchVersion(c *Ctx, versionID int64) error {
	_, err := c.DB().Exec(c, `UPDATE collections SET modified = now() WHERE id = (SELECT collection_id FROM collection_versions WHERE id = $1)`, versionID)
	return err
}

func signatureActivitySummary(c *Ctx, obj Obj) Obj {
	id, _ := api.ID(obj["id"])
	var name string
	c.DB().QueryRow(c, `SELECT t.name || '.' || c.name || ' ' || v.version || ' (' || s.pubkey_fingerprint || ')' FROM collection_version_signatures s
		JOIN collection_versions v ON v.id = s.collection_version_id JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id WHERE s.id = $1`, id).Scan(&name)
	return Obj{"id": id, "name": name}
}

// signAllPublished signs every published version with the active key, as
// a rotation does.
func signAllPublished(c *Ctx) error {
	s, err := loadHubSigner(c, c.App, c.DB())
	if err != nil {
		return err
	}
	rows, err := c.DB().Query(c, `SELECT rc.version_id FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
		WHERE r.name = $1 ORDER BY rc.version_id`, hubPublished)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.sign(c, c.DB(), id, c.User.UserID); err != nil {
			return fmt.Errorf("signing version %d: %w", id, err)
		}
	}
	return nil
}

// SignatureRequirement is what a project's jobs require of the
// collections they install: Count valid signatures from Keyring's keys.
type SignatureRequirement struct {
	Credential string
	Count      int
	// Keyring is in gpg's binary format, as ansible-galaxy's --keyring
	// reads it.
	Keyring []byte
}

// Digest identifies the requirement in the requirements cache's key.
func (s *SignatureRequirement) Digest() string {
	sum := sha256.Sum256(s.Keyring)
	return fmt.Sprintf("+%d:%s", s.Count, hex.EncodeToString(sum[:]))
}

// CollectionSignatureRequirement is a project's requirement of collection
// signatures, or nil when it has none.
func CollectionSignatureRequirement(ctx context.Context, app *api.App, project int64) (*SignatureRequirement, error) {
	var credID *int64
	var count int
	if err := app.DB.QueryRow(ctx, `SELECT collection_signature_validation_credential_id, collection_signature_count FROM unified_job_templates WHERE id = $1`,
		project).Scan(&credID, &count); err != nil || credID == nil {
		return nil, err
	}
	cred, err := ResolveCredential(ctx, app, *credID)
	if err != nil {
		return nil, err
	}
	key, _ := cred.Inputs["gpg_public_key"].(string)
	keyring, err := hubsign.Keyring(key)
	if err != nil {
		return nil, fmt.Errorf("the collection signature validation credential's public key can't be read: %w", err)
	}
	var name string
	app.DB.QueryRow(ctx, "SELECT name FROM credentials WHERE id = $1", *credID).Scan(&name)
	return &SignatureRequirement{Credential: name, Count: min(max(count, 1), maxSignatureCount), Keyring: keyring}, nil
}

func validateSignatureCount(c *Ctx, v any, data Obj) error {
	if n, ok := api.ID(v); !ok || n < 1 || n > maxSignatureCount {
		return fmt.Errorf("Ensure this value is between 1 and %d.", maxSignatureCount)
	}
	return nil
}

// requireCollectionSignatureCredential checks a project's collection
// signature validation credential: a GPG Public Key the user may use.
func requireCollectionSignatureCredential(c *Ctx, old, data Obj) error {
	const field = "collection_signature_validation_credential"
	v, set := data[field]
	if !set || (old != nil && fmt.Sprint(old[field]) == fmt.Sprint(v)) {
		return nil
	}
	if err := requireCredentialKind(c, field, v, "cryptography"); err != nil {
		return err
	}
	return api.RequirePermID(c, field, "credentials", v, "use")
}

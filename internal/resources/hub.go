package resources

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/blob"
	"github.com/damhau/awxnext/internal/jobs"
)

// The collection hub (roadmap M13): collections in namespaces that
// organizations own, served on Galaxy's v3 API (hub_api.go) so that
// ansible-galaxy and ansible-builder use it unchanged.

const (
	hubPublished = "published"
	// HubTokenPrefix starts the read tokens minted for a requirements
	// install, so the hub tells them from personal tokens.
	HubTokenPrefix = "awxhub_"
	// HubCredentialName is the managed Galaxy credential of the hub's
	// published repository; its URL is a path, completed for each run.
	HubCredentialName = "Collection Hub"
	HubCredentialPath = "/api/galaxy/content/" + hubPublished + "/"
)

// galaxyNameRe is ansible-galaxy's rule for namespaces and collection
// names, without the double underscore (checked apart: RE2 has no
// lookahead).
var galaxyNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validGalaxyName(s string) bool {
	return galaxyNameRe.MatchString(s) && !strings.Contains(s, "__")
}

func registerCollectionNamespaces() {
	api.Register(&Resource{
		Name:        "collection_namespaces",
		Type:        "collection_namespace",
		Table:       "collection_namespaces",
		Verbose:     "Collection Namespace",
		Ordering:    "name",
		ContentType: "collectionnamespace",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true,
				Help: "The organization that owns the namespace: its members see and install its collections."},
			{Name: "company", Type: api.TString, Default: "", MaxLength: 64},
			{Name: "email", Type: api.TString, Default: "", MaxLength: 256},
			{Name: "avatar_url", Type: api.TString, Default: "", MaxLength: 256},
			{Name: "auto_approve", Type: api.TBool, Default: false,
				Help: "Uploads to the namespace are published at once, without waiting for an approver."},
		},
		BeforeSave: saveCollectionNamespace,
		BeforeDelete: func(c *Ctx, obj Obj) error {
			if n := countQuery(c, "SELECT count(*) FROM collections WHERE namespace_id = $1", obj["id"]); n > 0 {
				return api.BadRequest(fmt.Sprintf("The namespace has %d collections: delete them first.", n))
			}
			return nil
		},
	})
}

func saveCollectionNamespace(c *Ctx, old, data Obj) error {
	name, given := data["name"]
	if !given {
		return nil
	}
	if !validGalaxyName(str(name)) {
		return api.FieldError("name", "A namespace name has lowercase letters, digits and single underscores, starts with a letter, and has at most 64 characters.")
	}
	if old != nil && str(old["name"]) != str(name) && countQuery(c, "SELECT count(*) FROM collections WHERE namespace_id = $1", old["id"]) > 0 {
		return api.FieldError("name", "The namespace has collections: it can't be renamed.")
	}
	return nil
}

// hubStore opens the hub's storage of a kind from the HUB_STORAGE_*
// settings.
func hubStore(app *api.App) func(ctx context.Context, kind string) (blob.Store, error) {
	return blobStore(app, "HUB_STORAGE", "collection hub")
}

// MintHubToken makes a read token of the collections of an organization's
// namespaces, for one requirements install of a job. revoke deletes it; it
// expires after ttl otherwise.
func MintHubToken(ctx context.Context, app *api.App, org, job int64, ttl time.Duration) (token string, revoke func(), err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = HubTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash := agent.HashToken(token)
	if _, err := app.DB.Exec(ctx, `INSERT INTO hub_tokens (token_hash, organization_id, unified_job_id, expires)
		VALUES ($1, $2, nullif($3, 0), now() + make_interval(secs => $4))`, hash, org, job, ttl.Seconds()); err != nil {
		return "", nil, err
	}
	revoke = func() {
		if _, err := app.DB.Exec(context.Background(), "DELETE FROM hub_tokens WHERE token_hash = $1", hash); err != nil {
			slog.Warn("revoking a collection hub token", "job", job, "err", err)
		}
	}
	return token, revoke, nil
}

// MaintainHub deletes expired hub tokens, old imports and the stored
// artifacts of deleted collection versions.
func MaintainHub(ctx context.Context, m *jobs.Manager) {
	db := m.DB()
	if _, err := db.Exec(ctx, "DELETE FROM hub_tokens WHERE expires < now()"); err != nil && ctx.Err() == nil {
		slog.Warn("deleting expired collection hub tokens", "err", err)
	}
	maintainImports(ctx, db)
	rows, err := db.Query(ctx, "SELECT id, storage, location FROM collection_object_deletions ORDER BY id LIMIT 1000")
	if err != nil {
		return
	}
	type gone struct {
		id                int64
		storage, location string
	}
	var list []gone
	for rows.Next() {
		var g gone
		if rows.Scan(&g.id, &g.storage, &g.location) == nil {
			list = append(list, g)
		}
	}
	rows.Close()
	open := hubStore(m.App)
	stores := map[string]blob.Store{}
	for _, g := range list {
		store, ok := stores[g.storage]
		if !ok {
			if store, err = open(ctx, g.storage); err != nil {
				slog.Warn("collection hub storage", "storage", g.storage, "err", err)
			}
			stores[g.storage] = store
		}
		if store == nil {
			continue
		}
		if err := store.Delete(ctx, g.location); err != nil {
			slog.Warn("deleting a collection artifact", "location", g.location, "err", err)
			continue
		}
		db.Exec(ctx, "DELETE FROM collection_object_deletions WHERE id = $1", g.id)
	}
}

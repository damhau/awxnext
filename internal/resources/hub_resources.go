package resources

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
)

// The hub's collections, versions and imports on /api/v2/, for the UI's
// approval queue (roadmap M13-05): a collection is seen by whoever reads
// its published versions, a staged or rejected version by its namespace's
// curators.

// publishedSQL is true when the version (alias v) is in a repository
// besides those of review.
const publishedSQL = `EXISTS (SELECT 1 FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
	WHERE rc.version_id = %s AND r.name NOT IN ('staging', 'rejected'))`

func namespacesWhere(c *Ctx, arg ArgFunc, pred func(c *Ctx, arg ArgFunc) string) string {
	return "(SELECT t.id FROM collection_namespaces t WHERE " + pred(c, arg) + ")"
}

func viewNamespaces(c *Ctx, arg ArgFunc) string {
	return api.ViewPredicate(c, api.Lookup("collection_namespaces"), arg)
}

// namespacePerm is a permission on a namespace, as its collections and
// versions take it; a request asks each once.
func namespacePerm(c *Ctx, nsID int64, perm string) (bool, error) {
	key := fmt.Sprintf("hub_perm:%d:%s", nsID, perm)
	if ok, cached := c.Extra[key].(bool); cached {
		return ok, nil
	}
	ok, err := api.HasPermID(c, "collection_namespaces", nsID, perm)
	if errors.Is(err, api.ErrNotFound) {
		return false, nil
	}
	if err == nil && c.Extra != nil {
		c.Extra[key] = ok
	}
	return ok, err
}

func requireNamespacePerm(c *Ctx, nsID int64, perm string) error {
	ok, err := namespacePerm(c, nsID, perm)
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

func versionNamespace(c *Ctx, collection any) int64 {
	var ns int64
	c.DB().QueryRow(c, "SELECT namespace_id FROM collections WHERE id = $1", collection).Scan(&ns)
	return ns
}

func registerCollections() {
	namespaces := api.Lookup("collection_namespaces")
	namespaces.Related = func(c *Ctx, obj Obj, rel map[string]any) {
		rel["collections"] = fmt.Sprintf("/api/v2/collections/?namespace=%v", obj["id"])
		rel["collection_imports"] = fmt.Sprintf("/api/v2/collection_imports/?namespace=%v", obj["id"])
	}

	collections := api.Register(&Resource{
		Name:          "collections",
		Type:          "collection",
		Table:         "collections",
		Verbose:       "Collection",
		Ordering:      "name",
		NoName:        true,
		NoTimestamps:  true,
		NoCreate:      true,
		SearchFields:  []string{"name"},
		SummaryFields: []string{"id", "name", "deprecated"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true, Help: "When a version last entered or left a repository."},
			{Name: "namespace", Type: api.TFK, Ref: "collection_namespaces", ReadOnly: true},
			{Name: "name", Type: api.TString, ReadOnly: true},
			{Name: "deprecated", Type: api.TBool, Default: false,
				Help: "ansible-galaxy warns when it installs a deprecated collection; its owners deprecate it."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return fmt.Sprintf(`(t.namespace_id IN %s OR (t.namespace_id IN %s AND EXISTS (SELECT 1 FROM collection_versions v WHERE v.collection_id = t.id AND `+publishedSQL+`)))`,
				namespacesWhere(c, arg, curatorPredicate), namespacesWhere(c, arg, viewNamespaces), "v.id")
		},
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			if perm == "view" {
				return true, nil
			}
			ns, _ := api.ID(obj["namespace"])
			return namespacePerm(c, ns, perm)
		},
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			ns, _ := api.ID(obj["namespace"])
			change, _ := namespacePerm(c, ns, "change")
			del, _ := namespacePerm(c, ns, "delete")
			return map[string]bool{"edit": change, "delete": del}
		},
		Related: func(c *Ctx, obj Obj, rel map[string]any) {
			rel["versions"] = fmt.Sprintf("/api/v2/collection_versions/?collection=%v", obj["id"])
		},
		Actions: []*api.Action{{Name: "used_by", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			rows, err := c.DB().Query(c, "SELECT id FROM collection_versions WHERE collection_id = $1", id)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				api.WriteError(w, err)
				return
			}
			all := map[int64]bool{}
			for _, v := range ids {
				all[v] = true
			}
			usedByCollection(c, w, id, all)
		}}},
	})
	collections.CanDelete = func(c *Ctx, obj Obj) error {
		ns, _ := api.ID(obj["namespace"])
		return requireNamespacePerm(c, ns, "delete")
	}
	collections.BeforeDelete = func(c *Ctx, obj Obj) error {
		id, _ := api.ID(obj["id"])
		if _, err := c.DB().Exec(c, "SELECT id FROM collections WHERE id = $1 FOR UPDATE", id); err != nil {
			return err
		}
		rows, err := c.DB().Query(c, "SELECT id FROM collection_versions WHERE collection_id = $1", id)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		all := map[int64]bool{}
		for _, v := range ids {
			all[v] = true
		}
		return refuseIfNeeded(c, id, all, str(collectionActivitySummary(c, obj)["name"]))
	}
	collections.ActivitySummary = collectionActivitySummary

	versions := api.Register(&Resource{
		Name:          "collection_versions",
		Type:          "collection_version",
		Table:         "collection_versions",
		Verbose:       "Collection Version",
		Ordering:      "-id",
		NoName:        true,
		NoTimestamps:  true,
		NoCreate:      true,
		NoUpdate:      true,
		SearchFields:  []string{"version", "filename"},
		SummaryFields: []string{"id", "version"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true, Help: "When the version was uploaded."},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "created_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true, Help: "Who uploaded the version."},
			{Name: "collection", Type: api.TFK, Ref: "collections", ReadOnly: true},
			{Name: "version", Type: api.TString, ReadOnly: true},
			{Name: "requires_ansible", Type: api.TString, ReadOnly: true},
			{Name: "metadata", Type: api.TJSON, ReadOnly: true, Help: "The collection_info of the artifact's MANIFEST.json."},
			{Name: "filename", Type: api.TString, ReadOnly: true},
			{Name: "sha256", Type: api.TString, ReadOnly: true},
			{Name: "size", Type: api.TInt, ReadOnly: true},
			{Name: "reviewed_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true, Help: "Who last approved or rejected the version."},
			{Name: "reviewed", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return fmt.Sprintf(`(t.collection_id IN (SELECT c.id FROM collections c WHERE c.namespace_id IN %s)
				OR (`+publishedSQL+` AND t.collection_id IN (SELECT c.id FROM collections c WHERE c.namespace_id IN %s)))`,
				namespacesWhere(c, arg, curatorPredicate), "t.id", namespacesWhere(c, arg, viewNamespaces))
		},
		ParamFilters: map[string]func(c *Ctx, value string, arg ArgFunc) (string, error){
			"repository": func(c *Ctx, value string, arg ArgFunc) (string, error) {
				return `EXISTS (SELECT 1 FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
					WHERE rc.version_id = t.id AND r.name = ` + arg(value) + `)`, nil
			},
			"namespace": func(c *Ctx, value string, arg ArgFunc) (string, error) {
				return "t.collection_id IN (SELECT c.id FROM collections c WHERE c.namespace_id::text = " + arg(value) + ")", nil
			},
		},
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			if perm == "view" {
				return true, nil
			}
			return namespacePerm(c, versionNamespace(c, obj["collection"]), perm)
		},
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			ns := versionNamespace(c, obj["collection"])
			approve, _ := namespacePerm(c, ns, "approve")
			del, _ := namespacePerm(c, ns, "delete")
			return map[string]bool{"approve": approve, "delete": del}
		},
		Summary:         versionSummaries,
		ActivitySummary: versionActivitySummary,
		CanDelete: func(c *Ctx, obj Obj) error {
			return requireNamespacePerm(c, versionNamespace(c, obj["collection"]), "delete")
		},
		BeforeDelete: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			coll, _ := api.ID(obj["collection"])
			if _, err := c.DB().Exec(c, "SELECT id FROM collections WHERE id = $1 FOR UPDATE", coll); err != nil {
				return err
			}
			return refuseIfNeeded(c, coll, map[int64]bool{id: true}, str(versionActivitySummary(c, obj)["name"]))
		},
		AfterDelete: func(c *Ctx, obj Obj) error {
			coll, _ := api.ID(obj["collection"])
			tag, err := c.DB().Exec(c, `DELETE FROM collections c WHERE c.id = $1 AND NOT EXISTS (SELECT 1 FROM collection_versions v WHERE v.collection_id = c.id)`, coll)
			if err != nil || tag.RowsAffected() > 0 {
				return err
			}
			_, err = c.DB().Exec(c, "UPDATE collections SET modified = now() WHERE id = $1", coll)
			return err
		},
	})
	review := func(dest string) func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		return func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			src := versionRepository(c, id)
			if src == "" {
				api.WriteError(w, api.BadRequest("The version is in no repository of review."))
				return
			}
			if err := moveCollectionVersion(c, id, src, dest); err != nil {
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
		}
	}
	versions.Actions = []*api.Action{
		{Name: "approve", Methods: []string{"POST"}, Perm: "approve", Handler: review(hubPublished)},
		{Name: "reject", Methods: []string{"POST"}, Perm: "approve", Handler: review(hubRejected)},
		{Name: "used_by", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			coll, _ := api.ID(obj["collection"])
			usedByCollection(c, w, coll, map[int64]bool{id: true})
		}},
	}

	api.Register(&Resource{
		Name:          "collection_imports",
		Type:          "collection_import",
		Table:         "collection_imports",
		Verbose:       "Collection Import",
		Ordering:      "-id",
		NoName:        true,
		NoTimestamps:  true,
		ReadOnly:      true,
		NoActivity:    true,
		SearchFields:  []string{"filename", "namespace_name", "collection_name"},
		SummaryFields: []string{"id", "state"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "created_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true, Help: "Who uploaded the artifact."},
			{Name: "started", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "finished", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "state", Type: api.TChoice, ReadOnly: true, Choices: []api.Choice{{Value: "running", Label: "Running"},
				{Value: "completed", Label: "Completed"}, {Value: "failed", Label: "Failed"}}},
			{Name: "filename", Type: api.TString, ReadOnly: true},
			{Name: "namespace", Type: api.TFK, Ref: "collection_namespaces", ReadOnly: true, Nullable: true,
				Help: "The namespace, once the checks found the uploader may upload there."},
			{Name: "namespace_name", Type: api.TString, ReadOnly: true, Help: "The namespace MANIFEST.json names."},
			{Name: "collection_name", Type: api.TString, ReadOnly: true},
			{Name: "version", Type: api.TString, ReadOnly: true},
			{Name: "collection_version", Type: api.TFK, Ref: "collection_versions", ReadOnly: true, Nullable: true,
				Help: "The version the import stored."},
			{Name: "messages", Type: api.TJSON, ReadOnly: true, Help: "The import's log: each check, with its level and time."},
			{Name: "error", Type: api.TJSON, ReadOnly: true, Nullable: true, Help: "Why the import failed: a code and a description."},
		},
		View: importVisible,
	})
}

// versionSummaries adds each version's namespace, repositories and
// latest import to its summary fields.
func versionSummaries(c *Ctx, objs []Obj) error {
	for _, o := range objs {
		sf, ok := o["summary_fields"].(Obj)
		if !ok {
			sf = Obj{}
			o["summary_fields"] = sf
		}
		id, _ := api.ID(o["id"])
		var nsID int64
		var nsName string
		if err := c.DB().QueryRow(c, `SELECT t.id, t.name FROM collection_versions v JOIN collections c ON c.id = v.collection_id
			JOIN collection_namespaces t ON t.id = c.namespace_id WHERE v.id = $1`, id).Scan(&nsID, &nsName); err != nil {
			return err
		}
		sf["namespace"] = Obj{"id": nsID, "name": nsName}
		rows, err := c.DB().Query(c, `SELECT r.name FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
			WHERE rc.version_id = $1 ORDER BY r.id`, id)
		if err != nil {
			return err
		}
		repos, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		sf["repositories"] = repos
		var impID int64
		var state string
		err = c.DB().QueryRow(c, "SELECT id, state FROM collection_imports WHERE collection_version_id = $1 ORDER BY id DESC LIMIT 1", id).Scan(&impID, &state)
		switch {
		case err == nil:
			sf["collection_import"] = Obj{"id": impID, "state": state}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	return nil
}

func versionActivitySummary(c *Ctx, obj Obj) Obj {
	id, _ := api.ID(obj["id"])
	var name string
	c.DB().QueryRow(c, `SELECT t.name || '.' || c.name || ' ' || v.version FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id WHERE v.id = $1`, id).Scan(&name)
	return Obj{"id": id, "name": name}
}

func collectionActivitySummary(c *Ctx, obj Obj) Obj {
	id, _ := api.ID(obj["id"])
	var name string
	c.DB().QueryRow(c, `SELECT t.name || '.' || c.name FROM collections c JOIN collection_namespaces t ON t.id = c.namespace_id
		WHERE c.id = $1`, id).Scan(&name)
	return Obj{"id": id, "name": name}
}

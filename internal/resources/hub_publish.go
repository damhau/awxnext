package resources

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// Publishing to the hub (roadmap M13-02): uploads on Galaxy's v3 API land
// in staging, the namespace's approvers move them to published or
// rejected, and its owners deprecate and delete them.

const (
	hubStaging  = "staging"
	hubRejected = "rejected"
	// What a multipart upload holds besides the artifact: part headers and
	// the sha256 field.
	uploadHeadroom = 64 << 10
	maxUploadLimit = 1 << 40
	// An import still running after this was cut short by a restart.
	importStale = time.Hour
	// Failed imports, and those whose version is gone, are kept this long.
	importKeep = 30 * 24 * time.Hour
)

// hubReview are the repositories a version moves between as it is
// reviewed; staging and rejected are read by the namespace's curators only.
var hubReview = map[string]bool{hubStaging: true, hubPublished: true, hubRejected: true}

func curatedOnly(repo string) bool { return repo == hubStaging || repo == hubRejected }

// curatorPredicate is the condition on namespaces (alias t) whose staging
// and rejected collections the principal reads: who may upload, approve
// or change there, the organization's auditors, and system auditors.
func curatorPredicate(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	a := rbac.ArgFunc(arg)
	parts := []string{c.User.Predicate(a, "organization", "audit_organization", rbac.Scope{ID: "t.organization_id"})}
	s := api.Lookup("collection_namespaces").Scope("t")
	for _, perm := range []string{"upload", "approve", "change"} {
		parts = append(parts, c.User.Predicate(a, "collectionnamespace", perm+"_collectionnamespace", s))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// namespaceReadable is the condition on namespaces (alias t) whose
// collections in a repository the principal reads.
func namespaceReadable(c *Ctx, repo string, arg ArgFunc) string {
	if curatedOnly(repo) {
		return curatorPredicate(c, arg)
	}
	return api.ViewPredicate(c, api.Lookup("collection_namespaces"), arg)
}

func installHubPublishing(app *api.App) {
	for _, p := range []string{
		galaxyRoot + "v3/artifacts/collections/",
		galaxyRoot + "content/{repo}/v3/artifacts/collections/",
		galaxyPlugin + "{repo}/collections/artifacts/",
	} {
		app.HandleAnonymous("POST", p, galaxy(galaxyUpload))
	}
	app.HandleAnonymous("GET", galaxyRoot+"v3/imports/collections/{id}/", galaxy(galaxyImport))
	app.HandleAnonymous("GET", galaxyRoot+"content/{repo}/v3/imports/collections/{id}/", galaxy(galaxyImport))
	app.HandleAnonymous("POST", galaxyRoot+"v3/collections/{namespace}/{name}/versions/{version}/move/{src}/{dest}/", galaxy(galaxyMove))
	for _, base := range []string{
		galaxyPlugin + "{repo}/collections/index",
		galaxyRoot + "content/{repo}/v3/collections",
		galaxyRoot + "v3/collections",
	} {
		app.HandleAnonymous("PATCH", base+"/{namespace}/{name}/", galaxy(galaxyDeprecate))
		app.HandleAnonymous("DELETE", base+"/{namespace}/{name}/", galaxy(galaxyDeleteCollection))
		app.HandleAnonymous("DELETE", base+"/{namespace}/{name}/versions/{version}/", galaxy(galaxyDeleteVersion))
	}
}

// writer reports whether the caller may change the hub, answering why
// not when it may not.
func (g *galaxyCall) writer() bool {
	switch {
	case g.tokenOrg > 0:
		galaxyError(g.w, 403, "permission_denied", "This token only reads collections.")
	case g.User.TokenScope == "read":
		galaxyError(g.w, 403, "permission_denied", "A read-scoped token only reads.")
	default:
		return true
	}
	return false
}

// failAPI answers an error of the API's engine in Galaxy's format.
func (g *galaxyCall) failAPI(err error) {
	var ae *api.Error
	if !errors.As(err, &ae) {
		g.fail(err)
		return
	}
	detail := ae.Error()
	if body, ok := ae.Body.(Obj); ok {
		if d, ok := body["detail"].(string); ok {
			detail = d
		}
	}
	code := "invalid"
	switch ae.Status {
	case 403:
		code = "permission_denied"
	case 404:
		code = "not_found"
	}
	galaxyError(g.w, ae.Status, code, detail)
}

// uploadFault is why an upload was refused before its import started.
type uploadFault struct {
	status       int
	code, detail string
}

func (f *uploadFault) Error() string { return f.detail }

func galaxyUpload(g *galaxyCall) {
	if !g.writer() {
		return
	}
	data, filename, sum, err := readUpload(g.w, g.r, int64(g.App.Settings.Int(g, "HUB_MAX_UPLOAD_SIZE")))
	if err != nil {
		var f *uploadFault
		if errors.As(err, &f) {
			galaxyError(g.w, f.status, f.code, f.detail)
		} else {
			g.fail(err)
		}
		return
	}
	id, err := runImport(g.Ctx, data, filename, sum)
	if err != nil {
		g.fail(err)
		return
	}
	api.WriteJSON(g.w, 202, Obj{"task": galaxyRoot + "v3/imports/collections/" + strconv.FormatInt(id, 10) + "/"})
}

// readUpload reads ansible-galaxy's multipart upload: the artifact in its
// file field, and its SHA-256 in sha256.
func readUpload(w http.ResponseWriter, r *http.Request, limit int64) (data []byte, filename, sum string, err error) {
	tooLarge := &uploadFault{413, "too_large", fmt.Sprintf("The artifact is larger than the hub takes: %s (HUB_MAX_UPLOAD_SIZE).", humanBytes(limit))}
	readErr := func(err error) error {
		if mb := (*http.MaxBytesError)(nil); errors.As(err, &mb) {
			return tooLarge
		}
		return &uploadFault{400, "invalid", "Reading the upload: " + err.Error()}
	}
	limit = min(limit, maxUploadLimit)
	// ansible-galaxy sends the artifact in base64: 4/3 of its size, and
	// line breaks.
	r.Body = http.MaxBytesReader(w, r.Body, limit+limit/2+uploadHeadroom)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", "", &uploadFault{400, "invalid", "Send the artifact as a multipart form, in its file field."}
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", "", readErr(err)
		}
		var body io.Reader = part
		if strings.EqualFold(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")), "base64") {
			body = base64.NewDecoder(base64.StdEncoding, part)
		}
		switch part.FormName() {
		case "file":
			if data != nil {
				return nil, "", "", &uploadFault{400, "invalid", "The form has two file fields: upload one artifact at a time."}
			}
			filename = uploadName(part.FileName())
			if data, err = io.ReadAll(io.LimitReader(body, limit+1)); err != nil {
				return nil, "", "", readErr(err)
			}
			if int64(len(data)) > limit {
				return nil, "", "", tooLarge
			}
		case "sha256":
			b, err := io.ReadAll(io.LimitReader(body, 256))
			if err != nil {
				return nil, "", "", readErr(err)
			}
			sum = strings.TrimSpace(string(b))
		}
		part.Close()
	}
	if data == nil {
		return nil, "", "", &uploadFault{400, "invalid", "The form has no file field: it holds the artifact."}
	}
	return data, filename, sum, nil
}

// uploadName is the base of the file name the client gave, which
// ansible-galaxy sends as the artifact's path on its side.
func uploadName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > 255 {
		name = name[:255]
	}
	return strings.ToValidUTF8(name, "")
}

// runImport records an upload's import, runs its checks and stores the
// version they accept. It returns the import's id: the import's outcome
// is in its log, which ansible-galaxy reads.
func runImport(c *Ctx, data []byte, filename, clientSum string) (int64, error) {
	var id int64
	if err := c.DB().QueryRow(c, `INSERT INTO collection_imports (created_by_id, started, filename) VALUES (nullif($1, 0), now(), $2) RETURNING id`,
		c.User.UserID, filename).Scan(&id); err != nil {
		return 0, err
	}
	log := &ImportLog{}
	out := importUpload(c, log, data, filename, clientSum)
	state := "completed"
	var errJSON []byte
	if out.err != nil {
		var ie *ImportError
		if !errors.As(out.err, &ie) {
			slog.Error("importing a collection", "import", id, "err", out.err)
			ie = &ImportError{Code: "internal_error", Message: "The import failed on the server's side: its log says why."}
		}
		log.add("ERROR", "%s", ie.Message)
		state = "failed"
		errJSON, _ = json.Marshal(Obj{"code": ie.Code, "description": ie.Message, "traceback": ""})
	}
	var ns, name, version string
	if out.a != nil {
		ns, name, version = out.a.Namespace, out.a.Name, out.a.Version
	}
	messages, _ := json.Marshal(log.Messages)
	_, err := c.DB().Exec(c, `UPDATE collection_imports SET state = $2, finished = now(), modified = now(), messages = $3, error = $4,
		namespace_id = nullif($5, 0), namespace_name = $6, collection_name = $7, version = $8, collection_version_id = nullif($9, 0) WHERE id = $1`,
		id, state, messages, errJSON, out.nsID, ns, name, version, out.versionID)
	return id, err
}

type importResult struct {
	a               *CollectionArtifact
	nsID, versionID int64
	err             error
}

// importUpload runs an upload's checks, logging each, and stores the
// version they accept: in staging, or in published when its namespace
// approves its uploads.
func importUpload(c *Ctx, log *ImportLog, data []byte, filename, clientSum string) (r importResult) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	log.info("Received %s: %s.", orNone(filename), humanBytes(int64(len(data))))
	switch {
	case clientSum == "":
		log.warn("The client sent no SHA-256 of the artifact: the upload is not checked against it.")
	case !strings.EqualFold(clientSum, digest):
		r.err = importErr("checksum_mismatch", "The artifact's SHA-256 is %s, the client sent %s: it changed on the way.", digest, clientSum)
		return r
	default:
		log.info("SHA-256: %s, as the client sent.", digest)
	}
	if r.a, r.err = checkCollectionArtifact(data, log); r.err != nil {
		return r
	}
	a := r.a
	var nsID int64
	var autoApprove bool
	var org string
	err := c.DB().QueryRow(c, `SELECT t.id, t.auto_approve, o.name FROM collection_namespaces t JOIN organizations o ON o.id = t.organization_id
		WHERE t.name = $1`, a.Namespace).Scan(&nsID, &autoApprove, &org)
	visible := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		r.err = err
		return r
	}
	if visible {
		if visible, r.err = api.CanView(c, api.Lookup("collection_namespaces"), nsID); r.err != nil {
			return r
		}
	}
	if !visible {
		r.err = importErr("namespace_not_found", "Namespace %q does not exist: an administrator of its organization creates it.", a.Namespace)
		return r
	}
	ok, err := api.HasPermID(c, "collection_namespaces", nsID, "upload")
	if err != nil {
		r.err = err
		return r
	}
	if !ok {
		r.err = importErr("permission_denied", "You may not upload to namespace %s: that takes its Upload or Admin role.", a.Namespace)
		return r
	}
	r.nsID = nsID
	log.info("Namespace: %s, of organization %s, takes your uploads.", a.Namespace, org)
	if versionTaken(c, c.App, nsID, a) {
		r.err = versionExists(a)
		return r
	}
	repo := hubStaging
	if autoApprove {
		repo = hubPublished
	}
	repoID, err := hubRepository(c, repo)
	if err != nil {
		r.err = err
		return r
	}
	if r.versionID, r.err = storeCollectionVersion(c, c.App, a, data, nsID, repoID, c.User.UserID); r.err != nil {
		return r
	}
	versions := api.Lookup("collection_versions")
	if err := api.Activity(c, "create", versions, Obj{"id": r.versionID}, Obj{"repository": repo}, nil, nil, ""); err != nil {
		slog.Warn("recording a collection upload", "version", r.versionID, "err", err)
	}
	if autoApprove {
		log.info("Published %s.%s %s: namespace %s approves its uploads.", a.Namespace, a.Name, a.Version, a.Namespace)
	} else {
		log.info("Staged %s.%s %s: it waits for an approver of namespace %s.", a.Namespace, a.Name, a.Version, a.Namespace)
	}
	return r
}

// importVisible is the condition on imports (alias t) the principal reads:
// its own, and those of the namespaces it curates.
func importVisible(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	if c.User.Anonymous() {
		return "FALSE"
	}
	return fmt.Sprintf("(t.created_by_id = %s OR t.namespace_id IN (SELECT t.id FROM collection_namespaces t WHERE %s))",
		arg(c.User.UserID), curatorPredicate(c, arg))
}

func galaxyImport(g *galaxyCall) {
	id, err := strconv.ParseInt(chi.URLParam(g.r, "id"), 10, 64)
	if err != nil || g.tokenOrg > 0 {
		galaxyNotFound(g.w)
		return
	}
	var state, ns, name, version string
	var created, modified time.Time
	var started, finished *time.Time
	var messages []any
	var failure map[string]any
	g.args = nil
	err = g.DB().QueryRow(g, `SELECT t.state, t.namespace_name, t.collection_name, t.version, t.created, t.modified, t.started, t.finished, t.messages, t.error
		FROM collection_imports t WHERE t.id = `+g.arg(id)+` AND `+importVisible(g.Ctx, g.arg), g.args...).
		Scan(&state, &ns, &name, &version, &created, &modified, &started, &finished, &messages, &failure)
	if errors.Is(err, pgx.ErrNoRows) {
		galaxyNotFound(g.w)
		return
	}
	if err != nil {
		g.fail(err)
		return
	}
	at := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return galaxyTime(*t)
	}
	if messages == nil {
		messages = []any{}
	}
	var errObj any
	if failure != nil {
		errObj = failure
	}
	api.WriteJSON(g.w, 200, Obj{
		"id":          strconv.FormatInt(id, 10),
		"state":       state,
		"created_at":  galaxyTime(created),
		"updated_at":  galaxyTime(modified),
		"started_at":  at(started),
		"finished_at": at(finished),
		"error":       errObj,
		"messages":    messages,
		"namespace":   ns,
		"name":        name,
		"version":     version,
	})
}

// moveCollectionVersion moves a version from one of the repositories of
// its review to another, recording who did. The caller checks that the
// principal may approve in the namespace.
func moveCollectionVersion(c *Ctx, versionID int64, src, dest string) error {
	if !hubReview[src] || !hubReview[dest] {
		return api.BadRequest(fmt.Sprintf("A version moves between %s, %s and %s.", hubStaging, hubPublished, hubRejected))
	}
	if src == dest {
		return api.BadRequest("The version is already in " + dest + ".")
	}
	return c.WithTx(func(c *Ctx) error {
		srcID, err := hubRepository(c, src)
		if err != nil {
			return err
		}
		destID, err := hubRepository(c, dest)
		if err != nil {
			return err
		}
		tag, err := c.DB().Exec(c, "DELETE FROM collection_repository_contents WHERE repository_id = $1 AND version_id = $2", srcID, versionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return api.Detail(404, "The version is not in "+src+".")
		}
		if _, err := c.DB().Exec(c, `INSERT INTO collection_repository_contents (repository_id, version_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, destID, versionID); err != nil {
			return err
		}
		var collID int64
		if err := c.DB().QueryRow(c, `UPDATE collection_versions SET reviewed_by_id = nullif($2, 0), reviewed = now(), modified = now()
			WHERE id = $1 RETURNING collection_id`, versionID, c.User.UserID).Scan(&collID); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE collections SET modified = now() WHERE id = $1", collID); err != nil {
			return err
		}
		return api.Activity(c, "update", api.Lookup("collection_versions"), Obj{"id": versionID},
			Obj{"repository": []string{src, dest}}, nil, nil, "")
	})
}

// versionRepository is the repository of its review a version is in.
func versionRepository(c *Ctx, versionID int64) string {
	var repo string
	c.DB().QueryRow(c, `SELECT r.name FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
		WHERE rc.version_id = $1 AND r.name = ANY($2) ORDER BY r.id LIMIT 1`, versionID, []string{hubStaging, hubPublished, hubRejected}).Scan(&repo)
	return repo
}

func approveDenied(namespace string) string {
	return fmt.Sprintf("You may not approve or reject the collections of namespace %s: that takes its Approve or Admin role.", namespace)
}

func galaxyMove(g *galaxyCall) {
	if !g.writer() {
		return
	}
	ns := chi.URLParam(g.r, "namespace")
	g.args = nil
	var vid, nsID int64
	err := g.DB().QueryRow(g, `SELECT v.id, t.id FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id
		WHERE t.name = `+g.arg(ns)+` AND c.name = `+g.arg(chi.URLParam(g.r, "name"))+` AND v.version = `+g.arg(chi.URLParam(g.r, "version"))+
		` AND `+curatorPredicate(g.Ctx, g.arg), g.args...).Scan(&vid, &nsID)
	if errors.Is(err, pgx.ErrNoRows) {
		galaxyNotFound(g.w)
		return
	}
	if err != nil {
		g.fail(err)
		return
	}
	ok, err := api.HasPermID(g.Ctx, "collection_namespaces", nsID, "approve")
	if err != nil {
		g.fail(err)
		return
	}
	if !ok {
		galaxyError(g.w, 403, "permission_denied", approveDenied(ns))
		return
	}
	if err := moveCollectionVersion(g.Ctx, vid, chi.URLParam(g.r, "src"), chi.URLParam(g.r, "dest")); err != nil {
		g.failAPI(err)
		return
	}
	// galaxy_ng's tasks; the move is done when this answers.
	api.WriteJSON(g.w, 202, Obj{"copy_task_id": nil, "remove_task_id": nil, "curate_all_synclist_repository_task_id": nil})
}

func galaxyDeprecate(g *galaxyCall) {
	if !g.writer() {
		return
	}
	body, err := api.ReadBody(g.r)
	if err != nil {
		g.failAPI(err)
		return
	}
	deprecated, ok := body["deprecated"].(bool)
	if !ok {
		galaxyError(g.w, 400, "invalid", `Send {"deprecated": true}, or false.`)
		return
	}
	hc, err := g.collection()
	if err != nil {
		g.fail(err)
		return
	}
	if hc == nil {
		galaxyNotFound(g.w)
		return
	}
	res := api.Lookup("collections")
	old, err := api.Get(g.Ctx, res, hc.id)
	if err == nil {
		_, err = api.Update(g.Ctx, res, old, Obj{"deprecated": deprecated})
	}
	if err != nil {
		g.failAPI(err)
		return
	}
	hc.deprecated = deprecated
	api.WriteJSON(g.w, 200, g.collectionObj(hc))
}

func galaxyDeleteVersion(g *galaxyCall) {
	if !g.writer() {
		return
	}
	_, v, err := g.version("t.name = $1 AND c.name = $2 AND v.version = $3",
		chi.URLParam(g.r, "namespace"), chi.URLParam(g.r, "name"), chi.URLParam(g.r, "version"))
	if err != nil {
		g.fail(err)
		return
	}
	if v == nil {
		galaxyNotFound(g.w)
		return
	}
	res := api.Lookup("collection_versions")
	obj, err := api.Get(g.Ctx, res, v.id)
	if err == nil {
		err = api.Delete(g.Ctx, res, obj)
	}
	if err != nil {
		g.failAPI(err)
		return
	}
	g.w.WriteHeader(204)
}

func galaxyDeleteCollection(g *galaxyCall) {
	if !g.writer() {
		return
	}
	hc, err := g.collection()
	if err != nil {
		g.fail(err)
		return
	}
	if hc == nil {
		galaxyNotFound(g.w)
		return
	}
	if err := deleteCollection(g.Ctx, hc.id); err != nil {
		g.failAPI(err)
		return
	}
	g.w.WriteHeader(204)
}

// deleteCollection deletes a collection with all its versions, when the
// principal may delete in its namespace and nothing needs them.
func deleteCollection(c *Ctx, collID int64) error {
	res := api.Lookup("collections")
	obj, err := api.Get(c, res, collID)
	if err != nil {
		return err
	}
	return api.Delete(c, res, obj)
}

// collectionDependent is what needs a collection: a project's requirement
// or a published version's dependency, with the range it asks for.
type collectionDependent struct {
	resource, typ string
	id            int64
	name, field   string
	spec          string
	// blocks: only versions being deleted meet it.
	blocks bool
}

// collectionDependents lists what the published versions of a collection
// in deleting meet: requirements of the projects of the namespace's
// organization, whose jobs install from the hub, and dependencies of
// published versions of other collections.
func collectionDependents(c *Ctx, collID int64, deleting map[int64]bool) ([]collectionDependent, error) {
	var fullname string
	var org int64
	if err := c.DB().QueryRow(c, `SELECT t.name || '.' || c.name, t.organization_id FROM collections c JOIN collection_namespaces t ON t.id = c.namespace_id
		WHERE c.id = $1`, collID).Scan(&fullname, &org); err != nil {
		return nil, err
	}
	type pv struct {
		id      int64
		version string
	}
	rows, err := c.DB().Query(c, `SELECT v.id, v.version FROM collection_versions v JOIN collection_repository_contents rc ON rc.version_id = v.id
		JOIN collection_repositories r ON r.id = rc.repository_id WHERE v.collection_id = $1 AND r.name = $2`, collID, hubPublished)
	if err != nil {
		return nil, err
	}
	published, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pv, error) {
		var v pv
		err := row.Scan(&v.id, &v.version)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	var out []collectionDependent
	add := func(d collectionDependent) {
		byDeleted, byKept := false, false
		for _, v := range published {
			if versionMeets(v.version, d.spec) {
				if deleting[v.id] {
					byDeleted = true
				} else {
					byKept = true
				}
			}
		}
		if byDeleted {
			d.blocks = !byKept
			out = append(out, d)
		}
	}
	collect := func(sql string, args []any, base collectionDependent) error {
		rows, err := c.DB().Query(c, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := base
			if err := rows.Scan(&d.id, &d.name, &d.spec); err != nil {
				return err
			}
			add(d)
		}
		return rows.Err()
	}
	if err := collect(`SELECT t.id, t.name, coalesce(r->>'version', '') FROM unified_job_templates t, jsonb_array_elements(t.collection_requirements) r
		WHERE t.type = 'project' AND t.organization_id = $1 AND r->>'name' = $2 ORDER BY t.name, t.id`, []any{org, fullname},
		collectionDependent{resource: "projects", typ: "project", field: "collection_requirements"}); err != nil {
		return nil, err
	}
	if err := collect(`SELECT v.id, t.name || '.' || c.name || ' ' || v.version, coalesce(v.metadata->'dependencies'->>$1, '')
		FROM collection_versions v JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id
		WHERE v.metadata->'dependencies' ? $1 AND v.collection_id <> $2 AND EXISTS (SELECT 1 FROM collection_repository_contents rc
			JOIN collection_repositories r ON r.id = rc.repository_id WHERE rc.version_id = v.id AND r.name = $3)
		ORDER BY 2`, []any{fullname, collID, hubPublished},
		collectionDependent{resource: "collection_versions", typ: "collection_version", field: "dependencies"}); err != nil {
		return nil, err
	}
	return out, nil
}

// refuseIfNeeded refuses to delete versions of a collection that are the
// only ones meeting a requirement or a dependency.
func refuseIfNeeded(c *Ctx, collID int64, deleting map[int64]bool, what string) error {
	deps, err := collectionDependents(c, collID, deleting)
	if err != nil {
		return err
	}
	var names []string
	hidden := 0
	for _, d := range deps {
		if !d.blocks {
			continue
		}
		if ok, _ := api.CanView(c, api.Lookup(d.resource), d.id); !ok {
			hidden++
			continue
		}
		label := d.typ + " " + d.name
		if d.spec != "" {
			label += " (" + d.spec + ")"
		}
		names = append(names, label)
	}
	if len(names) == 0 && hidden == 0 {
		return nil
	}
	sort.Strings(names)
	if len(names) > 5 {
		hidden += len(names) - 5
		names = names[:5]
	}
	if hidden > 0 {
		names = append(names, fmt.Sprintf("%d more", hidden))
	}
	return api.BadRequest(fmt.Sprintf("%s can't be deleted: nothing else published meets what needs it: %s.", what, strings.Join(names, ", ")))
}

// usedByCollection answers used_by for versions of a collection.
func usedByCollection(c *Ctx, w http.ResponseWriter, collID int64, deleting map[int64]bool) {
	deps, err := collectionDependents(c, collID, deleting)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := []Obj{}
	hidden := 0
	for _, d := range deps {
		if ok, _ := api.CanView(c, api.Lookup(d.resource), d.id); !ok {
			hidden++
			continue
		}
		effect := "none"
		if d.blocks {
			effect = "blocks"
		}
		out = append(out, Obj{"type": d.typ, "resource": d.resource, "id": d.id, "name": d.name, "field": d.field,
			"requirement": d.spec, "effect": effect})
	}
	api.WriteJSON(w, 200, Obj{"count": len(out), "hidden": hidden, "results": out})
}

// HubPublishedState is a digest of the published versions of an
// organization's namespaces, which its jobs install from: the
// requirements cache is keyed by it, so an approval, a rejection or a
// deletion is seen by the next install.
func HubPublishedState(ctx context.Context, app *api.App, org int64) (string, error) {
	var state string
	err := app.DB.QueryRow(ctx, `SELECT coalesce(md5(string_agg(v.id::text, ',' ORDER BY v.id)), '') FROM collection_versions v
		JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id
		JOIN collection_repository_contents rc ON rc.version_id = v.id JOIN collection_repositories r ON r.id = rc.repository_id
		WHERE t.organization_id = $1 AND r.name = $2`, org, hubPublished).Scan(&state)
	return state, err
}

// maintainImports fails the imports a restart cut short, and deletes the
// failed ones and those of deleted versions once old.
func maintainImports(ctx context.Context, db *pgxpool.Pool) {
	failure, _ := json.Marshal(Obj{"code": "interrupted", "description": "The server stopped during the import: upload the artifact again.", "traceback": ""})
	if _, err := db.Exec(ctx, `UPDATE collection_imports SET state = 'failed', finished = now(), modified = now(), error = $2
		WHERE finished IS NULL AND started < now() - make_interval(secs => $1)`, importStale.Seconds(), failure); err != nil && ctx.Err() == nil {
		slog.Warn("failing interrupted collection imports", "err", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM collection_imports WHERE finished < now() - make_interval(secs => $1)
		AND (state = 'failed' OR collection_version_id IS NULL)`, importKeep.Seconds()); err != nil && ctx.Err() == nil {
		slog.Warn("deleting old collection imports", "err", err)
	}
}

package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
)

// Galaxy's v3 API, in galaxy_ng's URLs: ansible-galaxy and ansible-builder
// point at /api/galaxy/content/<repository>/ (or /api/galaxy/ for the
// published repository) and find the rest from there.

const (
	galaxyRoot   = "/api/galaxy/"
	galaxyPlugin = galaxyRoot + "v3/plugin/ansible/content/"
	// Pulp's limit/offset pagination: 10 by default, ansible-galaxy asks for
	// 100.
	galaxyPageSize    = 10
	galaxyMaxPageSize = 100
)

func installHub(app *api.App) {
	app.HandleAnonymous("GET", galaxyRoot, galaxyRootView)
	app.HandleAnonymous("GET", galaxyRoot+"content/{repo}/", galaxyRootView)
	for _, base := range []string{
		galaxyPlugin + "{repo}/collections/index",
		galaxyRoot + "content/{repo}/v3/collections",
		galaxyRoot + "v3/collections",
	} {
		app.HandleAnonymous("GET", base+"/", galaxy(galaxyCollections))
		app.HandleAnonymous("GET", base+"/{namespace}/{name}/", galaxy(galaxyCollection))
		app.HandleAnonymous("GET", base+"/{namespace}/{name}/versions/", galaxy(galaxyVersions))
		app.HandleAnonymous("GET", base+"/{namespace}/{name}/versions/{version}/", galaxy(galaxyVersion))
		app.HandleAnonymous("GET", base+"/{namespace}/{name}/versions/{version}/docs-blob/", galaxy(galaxyDocsBlob))
	}
	// The API's paths take a trailing slash; ansible-galaxy names the file
	// it saves after the download URL's last segment, which has none.
	app.HandleAnonymous("GET", galaxyPlugin+"{repo}/collections/artifacts/{filename}/", galaxy(galaxyDownload))
	installHubPublishing(app)
	installHubKeyring(app)
}

func galaxyError(w http.ResponseWriter, status int, code, detail string) {
	api.WriteJSON(w, status, Obj{"errors": []Obj{{"status": strconv.Itoa(status), "code": code, "title": http.StatusText(status), "detail": detail}}})
}

func galaxyNotFound(w http.ResponseWriter) { galaxyError(w, 404, "not_found", "Not found.") }

func galaxyRootView(c *Ctx, w http.ResponseWriter, r *http.Request) {
	if repo := chi.URLParam(r, "repo"); repo != "" {
		if _, err := hubRepository(c, repo); err != nil {
			galaxyNotFound(w)
			return
		}
	}
	api.WriteJSON(w, 200, Obj{"available_versions": Obj{"v3": "v3/"}, "current_version": "v3",
		"server_version": api.ProductVersion, "description": "awxnext's collection hub"})
}

func hubRepository(c *Ctx, name string) (int64, error) {
	var id int64
	err := c.DB().QueryRow(c, "SELECT id FROM collection_repositories WHERE name = $1", name).Scan(&id)
	return id, err
}

// galaxyCall is a request to the hub, from a user or with a sync token.
type galaxyCall struct {
	*Ctx
	w      http.ResponseWriter
	r      *http.Request
	repo   string
	repoID int64
	// tokenOrg is the organization of a sync token, whose namespaces it reads.
	tokenOrg int64
	// remoteOrg is the organization of the repository's remote, whose
	// members and jobs read all of it; readsRemote: the caller is one.
	remoteOrg   int64
	readsRemote bool
	args        []any
}

func (g *galaxyCall) arg(v any) string {
	g.args = append(g.args, v)
	return fmt.Sprintf("$%d", len(g.args))
}

// visible is the condition on namespaces (alias t) whose collections in
// the repository the caller may read. Sync tokens read neither staging nor
// rejected. A remote's repository is its organization's.
func (g *galaxyCall) visible() string {
	if g.tokenOrg > 0 {
		switch {
		case curatedOnly(g.repo):
			return "FALSE"
		case g.remoteOrg == g.tokenOrg:
			return "TRUE"
		}
		return "t.organization_id = " + g.arg(g.tokenOrg)
	}
	if g.readsRemote {
		return "TRUE"
	}
	return namespaceReadable(g.Ctx, g.repo, g.arg)
}

// galaxy authenticates a hub request: a sync token, else what /api/v2/
// takes (a personal token as Token or Bearer, basic, a session). Anonymous
// callers read nothing.
func galaxy(h func(g *galaxyCall)) func(c *Ctx, w http.ResponseWriter, r *http.Request) {
	return func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		g := &galaxyCall{Ctx: c, w: w, r: r, repo: chi.URLParam(r, "repo")}
		if g.repo == "" {
			g.repo = hubPublished
		}
		scheme, cred, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		cred = strings.TrimSpace(cred)
		if s := strings.ToLower(scheme); (s == "token" || s == "bearer") && strings.HasPrefix(cred, HubTokenPrefix) {
			err := c.DB().QueryRow(c, "SELECT organization_id FROM hub_tokens WHERE token_hash = $1 AND expires > now()",
				agent.HashToken(cred)).Scan(&g.tokenOrg)
			if err != nil {
				galaxyError(w, 401, "authentication_failed", "Invalid token.")
				return
			}
		} else {
			p, err := api.Authenticate(r, w)
			var ae *api.Error
			switch {
			case errors.As(err, &ae):
				body, _ := ae.Body.(Obj)
				detail, _ := body["detail"].(string)
				galaxyError(w, ae.Status, "authentication_failed", detail)
				return
			case err != nil:
				api.WriteError(w, err)
				return
			case p == nil:
				galaxyError(w, 401, "not_authenticated", "Authentication credentials were not provided.")
				return
			}
			c.User = p
		}
		var remote int64
		if err := c.DB().QueryRow(c, `SELECT r.id, coalesce(r.remote_id, 0), coalesce(u.organization_id, 0) FROM collection_repositories r
			LEFT JOIN unified_job_templates u ON u.id = r.remote_id WHERE r.name = $1`, g.repo).Scan(&g.repoID, &remote, &g.remoteOrg); err != nil {
			galaxyNotFound(w)
			return
		}
		if remote != 0 && g.tokenOrg == 0 {
			ok, err := api.CanView(c, api.Lookup("collection_remotes"), remote)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			g.readsRemote = ok
		}
		h(g)
	}
}

// hubCollection is a collection of the repository the caller may read.
type hubCollection struct {
	id                int64
	namespace, name   string
	deprecated        bool
	created, modified time.Time
	versions          []hubVersion
	highest           string
}

type hubVersion struct {
	id                               int64
	version, requiresAnsible         string
	created, modified                time.Time
	metadata, manifest, files        map[string]any
	filename, sha256, storage, place string
	size                             int64
}

// collections lists the repository's collections the caller may read,
// with their versions in it, filtered by namespace and name when given.
func (g *galaxyCall) collections(namespace, name string) ([]*hubCollection, error) {
	g.args = nil
	where := []string{g.visible(), fmt.Sprintf(`EXISTS (SELECT 1 FROM collection_versions v JOIN collection_repository_contents rc ON rc.version_id = v.id
		WHERE v.collection_id = c.id AND rc.repository_id = %s)`, g.arg(g.repoID))}
	if namespace != "" {
		where = append(where, "t.name = "+g.arg(namespace))
	}
	if name != "" {
		where = append(where, "c.name = "+g.arg(name))
	}
	rows, err := g.DB().Query(g, `SELECT c.id, t.name, c.name, c.deprecated, c.created, c.modified FROM collections c
		JOIN collection_namespaces t ON t.id = c.namespace_id WHERE `+strings.Join(where, " AND ")+` ORDER BY t.name, c.name`, g.args...)
	if err != nil {
		return nil, err
	}
	var out []*hubCollection
	byID := map[int64]*hubCollection{}
	ids := []int64{}
	for rows.Next() {
		hc := &hubCollection{}
		if err := rows.Scan(&hc.id, &hc.namespace, &hc.name, &hc.deprecated, &hc.created, &hc.modified); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, hc)
		byID[hc.id] = hc
		ids = append(ids, hc.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}
	rows, err = g.DB().Query(g, `SELECT v.collection_id, v.version, v.created, v.modified, v.requires_ansible FROM collection_versions v
		JOIN collection_repository_contents rc ON rc.version_id = v.id WHERE rc.repository_id = $1 AND v.collection_id = ANY($2)`, g.repoID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var v hubVersion
		if err := rows.Scan(&cid, &v.version, &v.created, &v.modified, &v.requiresAnsible); err != nil {
			return nil, err
		}
		byID[cid].versions = append(byID[cid].versions, v)
	}
	for _, hc := range out {
		sort.Slice(hc.versions, func(i, j int) bool { return compareSemver(hc.versions[i].version, hc.versions[j].version) > 0 })
		hc.highest = highestVersion(hc.versions)
	}
	return out, rows.Err()
}

// highestVersion of versions sorted highest first is Pulp's: the highest
// stable release, else the highest pre-release.
func highestVersion(versions []hubVersion) string {
	for _, v := range versions {
		if p, ok := parseSemver(v.version); ok && len(p.pre) == 0 {
			return v.version
		}
	}
	if len(versions) > 0 {
		return versions[0].version
	}
	return ""
}

// collection is one collection the caller may read, or nil.
func (g *galaxyCall) collection() (*hubCollection, error) {
	ns, name := chi.URLParam(g.r, "namespace"), chi.URLParam(g.r, "name")
	if ns == "" || name == "" {
		return nil, nil
	}
	list, err := g.collections(ns, name)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

func (g *galaxyCall) fail(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	api.WriteError(g.w, err)
}

func galaxyPath(repo string, parts ...string) string {
	p := galaxyPlugin + url.PathEscape(repo) + "/collections/index/"
	for _, s := range parts {
		p += url.PathEscape(s) + "/"
	}
	return p
}

func galaxyTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

func (g *galaxyCall) collectionObj(hc *hubCollection) Obj {
	o := Obj{
		"href":           galaxyPath(g.repo, hc.namespace, hc.name),
		"namespace":      hc.namespace,
		"name":           hc.name,
		"deprecated":     hc.deprecated,
		"versions_url":   galaxyPath(g.repo, hc.namespace, hc.name, "versions"),
		"created_at":     galaxyTime(hc.created),
		"updated_at":     galaxyTime(hc.modified),
		"download_count": 0,
	}
	o["highest_version"] = Obj{"href": galaxyPath(g.repo, hc.namespace, hc.name, "versions", hc.highest), "version": hc.highest}
	return o
}

// page is Pulp's limit/offset pagination of n items, as galaxy_ng
// answers: meta, links relative to the server and data.
func (g *galaxyCall) page(n int) (offset, limit int, links Obj) {
	q := g.r.URL.Query()
	limit, offset = galaxyPageSize, 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, galaxyMaxPageSize)
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		offset = v
	}
	link := func(off int) string {
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(off))
		return g.r.URL.Path + "?" + q.Encode()
	}
	last := 0
	if n > 0 {
		last = (n - 1) / limit * limit
	}
	links = Obj{"first": link(0), "last": link(last), "next": nil, "previous": nil}
	if offset+limit < n {
		links["next"] = link(offset + limit)
	}
	if offset > 0 {
		links["previous"] = link(max(0, min(offset, n)-limit))
	}
	return offset, limit, links
}

func pageWindow(n, offset, limit int) (int, int) {
	start := min(offset, n)
	return start, min(start+limit, n)
}

func galaxyCollections(g *galaxyCall) {
	q := g.r.URL.Query()
	list, err := g.collections(q.Get("namespace"), q.Get("name"))
	if err != nil {
		g.fail(err)
		return
	}
	offset, limit, links := g.page(len(list))
	from, to := pageWindow(len(list), offset, limit)
	data := make([]Obj, 0, to-from)
	for _, hc := range list[from:to] {
		data = append(data, g.collectionObj(hc))
	}
	api.WriteJSON(g.w, 200, Obj{"meta": Obj{"count": len(list)}, "links": links, "data": data})
}

func galaxyCollection(g *galaxyCall) {
	hc, err := g.collection()
	switch {
	case err != nil:
		g.fail(err)
	case hc == nil:
		galaxyNotFound(g.w)
	default:
		api.WriteJSON(g.w, 200, g.collectionObj(hc))
	}
}

func galaxyVersions(g *galaxyCall) {
	hc, err := g.collection()
	if err != nil {
		g.fail(err)
		return
	}
	if hc == nil {
		galaxyNotFound(g.w)
		return
	}
	offset, limit, links := g.page(len(hc.versions))
	from, to := pageWindow(len(hc.versions), offset, limit)
	data := make([]Obj, 0, to-from)
	for _, v := range hc.versions[from:to] {
		data = append(data, Obj{
			"version":          v.version,
			"href":             galaxyPath(g.repo, hc.namespace, hc.name, "versions", v.version),
			"created_at":       galaxyTime(v.created),
			"updated_at":       galaxyTime(v.modified),
			"requires_ansible": nullIfEmpty(v.requiresAnsible),
			"marks":            []any{},
		})
	}
	api.WriteJSON(g.w, 200, Obj{"meta": Obj{"count": len(hc.versions)}, "links": links, "data": data})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// version loads a version of the repository the caller may read, by its
// collection and version, or by its artifact's file name.
func (g *galaxyCall) version(cond string, args ...any) (*hubCollection, *hubVersion, error) {
	g.args = args
	where := "rc.repository_id = " + g.arg(g.repoID) + " AND " + cond + " AND " + g.visible()
	rows, err := g.DB().Query(g, `SELECT c.id, t.name, c.name, v.id, v.version, v.requires_ansible, v.created, v.modified, v.metadata, v.manifest, v.files,
			v.filename, v.sha256, v.size, v.storage, v.location
		FROM collection_versions v JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id
		JOIN collection_repository_contents rc ON rc.version_id = v.id WHERE `+where, g.args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil, rows.Err()
	}
	hc, v := &hubCollection{}, &hubVersion{}
	err = rows.Scan(&hc.id, &hc.namespace, &hc.name, &v.id, &v.version, &v.requiresAnsible, &v.created, &v.modified, &v.metadata, &v.manifest, &v.files,
		&v.filename, &v.sha256, &v.size, &v.storage, &v.place)
	return hc, v, err
}

func galaxyVersion(g *galaxyCall) {
	hc, v, err := g.version("t.name = $1 AND c.name = $2 AND v.version = $3",
		chi.URLParam(g.r, "namespace"), chi.URLParam(g.r, "name"), chi.URLParam(g.r, "version"))
	if err != nil {
		g.fail(err)
		return
	}
	if v == nil {
		galaxyNotFound(g.w)
		return
	}
	meta := Obj{}
	for _, k := range []string{"authors", "contents", "dependencies", "description", "documentation", "homepage", "issues", "license", "repository", "tags"} {
		meta[k] = v.metadata[k]
	}
	for _, k := range []string{"authors", "contents", "license", "tags"} {
		if meta[k] == nil {
			meta[k] = []any{}
		}
	}
	if meta["dependencies"] == nil {
		meta["dependencies"] = Obj{}
	}
	signatures, err := versionSignatures(g, g.DB(), v.id)
	if err != nil {
		g.fail(err)
		return
	}
	api.WriteJSON(g.w, 200, Obj{
		"version":          v.version,
		"href":             galaxyPath(g.repo, hc.namespace, hc.name, "versions", v.version),
		"created_at":       galaxyTime(v.created),
		"updated_at":       galaxyTime(v.modified),
		"requires_ansible": nullIfEmpty(v.requiresAnsible),
		"marks":            []any{},
		"artifact":         Obj{"filename": v.filename, "sha256": v.sha256, "size": v.size},
		"collection":       Obj{"id": strconv.FormatInt(hc.id, 10), "name": hc.name, "href": galaxyPath(g.repo, hc.namespace, hc.name)},
		"download_url":     requestBase(g.r) + galaxyPlugin + url.PathEscape(g.repo) + "/collections/artifacts/" + url.PathEscape(v.filename),
		"name":             hc.name,
		"namespace":        Obj{"name": hc.namespace, "metadata_sha256": nil},
		"signatures":       signatures,
		"metadata":         meta,
		"git_url":          nil,
		"git_commit_sha":   nil,
		"manifest":         v.manifest,
		"files":            v.files,
	})
}

// requestBase is the scheme and host the client reached awxnext at, which
// ansible-galaxy sends its token to for the download.
func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

func galaxyDownload(g *galaxyCall) {
	_, v, err := g.version("v.filename = $1", chi.URLParam(g.r, "filename"))
	if err != nil {
		g.fail(err)
		return
	}
	if v == nil {
		galaxyNotFound(g.w)
		return
	}
	data, err := loadArtifact(g, g.App, g.DB(), v.id, v.storage, v.place)
	if errors.Is(err, pgx.ErrNoRows) {
		galaxyNotFound(g.w)
		return
	}
	if err != nil {
		g.fail(err)
		return
	}
	h := g.w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", v.filename))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	g.w.WriteHeader(200)
	g.w.Write(data)
}

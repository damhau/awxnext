package test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

// galaxyName is a unique namespace name, as Galaxy allows them.
func galaxyName(prefix string) string {
	seq++
	return fmt.Sprintf("%s%d_%d", prefix, time.Now().UnixNano()%1000000, seq)
}

// collectionTarball builds a collection artifact as ansible-galaxy
// collection build does: MANIFEST.json and FILES.json first, with every
// file's checksum, which ansible-galaxy checks when it installs.
func collectionTarball(t *testing.T, ns, name, version string, deps map[string]string, files map[string]string) []byte {
	t.Helper()
	dependencies := map[string]any{}
	for k, v := range deps {
		dependencies[k] = v
	}
	return collectionTarballInfo(t, ns, name, version, map[string]any{"dependencies": dependencies}, files)
}

// collectionTarballInfo is collectionTarball with collection_info's keys
// in info.
func collectionTarballInfo(t *testing.T, ns, name, version string, info map[string]any, files map[string]string) []byte {
	t.Helper()
	if files == nil {
		files = map[string]string{}
	}
	files["README.md"] = "# " + ns + "." + name + "\n"
	files["meta/runtime.yml"] = "requires_ansible: '>=2.15.0'\n"
	names := make([]string, 0, len(files))
	dirs := map[string]bool{}
	for n := range files {
		names = append(names, n)
		for d := filepath.Dir(n); d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		names = append(names, d+"/")
	}
	sort.Strings(names)
	entries := []map[string]any{{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}}
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			entries = append(entries, map[string]any{"name": strings.TrimSuffix(n, "/"), "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1})
			continue
		}
		sum := sha256.Sum256([]byte(files[n]))
		entries = append(entries, map[string]any{"name": n, "ftype": "file", "chksum_type": "sha256", "chksum_sha256": hex.EncodeToString(sum[:]), "format": 1})
	}
	filesJSON, _ := json.Marshal(map[string]any{"files": entries, "format": 1})
	filesSum := sha256.Sum256(filesJSON)
	collInfo := map[string]any{"namespace": ns, "name": name, "version": version, "authors": []string{"awxnext tests"},
		"readme": "README.md", "tags": []string{"test"}, "description": "A test collection", "license": []string{"GPL-3.0-or-later"},
		"license_file": nil, "dependencies": map[string]any{}, "repository": nil, "documentation": nil, "homepage": nil, "issues": nil}
	for k, v := range info {
		collInfo[k] = v
	}
	manifest, _ := json.Marshal(map[string]any{
		"collection_info":    collInfo,
		"file_manifest_file": map[string]any{"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": hex.EncodeToString(filesSum[:]), "format": 1},
		"format":             1,
	})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		tw.Write(data)
	}
	add("MANIFEST.json", manifest)
	add("FILES.json", filesJSON)
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(n, "/"), Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Now()})
			continue
		}
		add(n, []byte(files[n]))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func hubPublish(t *testing.T, data []byte) {
	t.Helper()
	if _, err := resources.ImportCollection(t.Context(), app, "published", data); err != nil {
		t.Fatalf("importing a collection: %v", err)
	}
}

// galaxyGet calls the hub with an Authorization header, or none.
func galaxyGet(t *testing.T, auth, path string) (int, map[string]any, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var body map[string]any
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		json.Unmarshal(raw, &body)
	}
	return res.StatusCode, body, raw
}

func patOf(t *testing.T, c *client) string {
	t.Helper()
	return must(t, c.post("tokens/", map[string]any{"description": "hub", "scope": "read"}), 201).Body["token"].(string)
}

func TestHubAPI(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	member, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": uid, "object_id": org}), 201)
	stranger, strangerID := newUser(t, a)

	for _, bad := range []string{"Bad", "a-b", "_ab", "a__b", "1ab", strings.Repeat("a", 65)} {
		must(t, a.post("collection_namespaces/", map[string]any{"name": bad, "organization": org}), 400)
	}
	ns := galaxyName("ns")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "company": "ACME"}), 201).id()
	for _, v := range []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-beta.1"} {
		hubPublish(t, collectionTarball(t, ns, "tools", v, nil, map[string]string{"plugins/modules/hello.py": "# hello " + v + "\n"}))
	}
	if _, err := resources.ImportCollection(t.Context(), app, "published", collectionTarball(t, ns, "tools", "1.0.0", nil, nil)); err == nil {
		t.Fatal("the same version was imported twice")
	}
	if _, err := resources.ImportCollection(t.Context(), app, "published", collectionTarball(t, galaxyName("none"), "tools", "1.0.0", nil, nil)); err == nil {
		t.Fatal("imported into a namespace that doesn't exist")
	}
	if _, err := resources.ImportCollection(t.Context(), app, "published", []byte("not a tarball")); err == nil {
		t.Fatal("imported garbage")
	}
	must(t, a.patch(fmt.Sprintf("collection_namespaces/%d/", nsID), map[string]any{"name": galaxyName("other")}), 400)
	must(t, a.delete(fmt.Sprintf("collection_namespaces/%d/", nsID)), 400)

	pat := patOf(t, member)
	coll := "/api/galaxy/content/published/v3/collections/" + ns + "/tools/"

	// Roots answer anyone, as galaxy_ng's: clients discover the API there.
	for _, root := range []string{"/api/galaxy/", "/api/galaxy/content/published/"} {
		if st, body, raw := galaxyGet(t, "", root); st != 200 || fmt.Sprint(body["available_versions"]) != "map[v3:v3/]" {
			t.Fatalf("%s: %d %s", root, st, raw)
		}
	}
	if st, _, _ := galaxyGet(t, "", "/api/galaxy/content/nope/"); st != 404 {
		t.Fatalf("unknown repository: %d", st)
	}
	if st, body, raw := galaxyGet(t, "", coll); st != 401 || body["errors"] == nil {
		t.Fatalf("anonymous: %d %s", st, raw)
	}
	if st, _, _ := galaxyGet(t, "Token nope", coll); st != 401 {
		t.Fatalf("bad token: %d", st)
	}

	// The member reads it with a personal token, as Token or Bearer.
	for _, auth := range []string{"Token " + pat, "Bearer " + pat} {
		st, body, raw := galaxyGet(t, auth, coll)
		if st != 200 || body["name"] != "tools" || body["namespace"] != ns || body["highest_version"].(map[string]any)["version"] != "1.10.0" ||
			body["updated_at"] == "" {
			t.Fatalf("collection with %s: %d %s", strings.Fields(auth)[0], st, raw)
		}
	}
	auth := "Token " + pat
	// Versions, highest first, as Pulp paginates them: limit and offset.
	st, body, raw := galaxyGet(t, auth, coll+"versions/?limit=2")
	if st != 200 || fmt.Sprint(body["meta"]) != "map[count:4]" {
		t.Fatalf("versions: %d %s", st, raw)
	}
	var got []string
	next := ""
	for page := 0; page < 3; page++ {
		for _, v := range body["data"].([]any) {
			got = append(got, v.(map[string]any)["version"].(string))
		}
		n, _ := body["links"].(map[string]any)["next"].(string)
		if n == "" {
			break
		}
		next = n
		if !strings.HasPrefix(next, "/api/galaxy/") {
			t.Fatalf("next link: %q", next)
		}
		st, body, raw = galaxyGet(t, auth, next)
		if st != 200 {
			t.Fatalf("next page: %d %s", st, raw)
		}
	}
	if strings.Join(got, " ") != "2.0.0-beta.1 1.10.0 1.2.0 1.0.0" {
		t.Fatalf("versions in order: %v", got)
	}
	// galaxy_ng's canonical path and the default repository's give the same.
	for _, p := range []string{
		"/api/galaxy/v3/plugin/ansible/content/published/collections/index/" + ns + "/tools/versions/1.2.0/",
		"/api/galaxy/v3/collections/" + ns + "/tools/versions/1.2.0/",
		coll + "versions/1.2.0/",
	} {
		st, body, raw := galaxyGet(t, auth, p)
		if st != 200 || body["version"] != "1.2.0" || body["namespace"].(map[string]any)["name"] != ns || body["collection"].(map[string]any)["name"] != "tools" {
			t.Fatalf("%s: %d %s", p, st, raw)
		}
	}
	_, version, _ := galaxyGet(t, auth, coll+"versions/1.2.0/")
	download, _ := version["download_url"].(string)
	if !strings.HasPrefix(download, srv.URL+"/api/galaxy/v3/plugin/ansible/content/published/collections/artifacts/") ||
		!strings.HasSuffix(download, "/"+ns+"-tools-1.2.0.tar.gz") || version["requires_ansible"] != ">=2.15.0" {
		t.Fatalf("version: %v", version)
	}
	if deps := version["metadata"].(map[string]any)["dependencies"]; fmt.Sprint(deps) != "map[]" {
		t.Fatalf("dependencies: %v", deps)
	}
	path := strings.TrimPrefix(download, srv.URL)
	st, _, data := galaxyGet(t, auth, path)
	sum := sha256.Sum256(data)
	if st != 200 || hex.EncodeToString(sum[:]) != version["artifact"].(map[string]any)["sha256"] {
		t.Fatalf("download: %d, %d bytes", st, len(data))
	}
	if st, body, _ := galaxyGet(t, auth, "/api/galaxy/v3/plugin/ansible/content/published/collections/index/?namespace="+ns); st != 200 || fmt.Sprint(body["meta"]) != "map[count:1]" {
		t.Fatalf("collections: %d %v", st, body)
	}
	if st, _, _ := galaxyGet(t, auth, "/api/galaxy/content/nope/v3/collections/"+ns+"/tools/"); st != 404 {
		t.Fatalf("unknown repository: %d", st)
	}

	// A user without rights on the namespace's organization finds nothing:
	// 404, as for a collection that doesn't exist, so that clients go on to
	// their next server and namespaces of other organizations stay unknown.
	spat := "Token " + patOf(t, stranger)
	for _, p := range []string{coll, coll + "versions/", coll + "versions/1.2.0/", path} {
		if st, body, raw := galaxyGet(t, spat, p); st != 404 || body["errors"] == nil {
			t.Fatalf("stranger, %s: %d %s", p, st, raw)
		}
	}
	if st, body, _ := galaxyGet(t, spat, "/api/galaxy/v3/plugin/ansible/content/published/collections/index/?namespace="+ns); st != 200 || fmt.Sprint(body["meta"]) != "map[count:0]" {
		t.Fatalf("stranger's collections: %d %v", st, body)
	}
	must(t, stranger.get(fmt.Sprintf("collection_namespaces/%d/", nsID)), 403)
	must(t, member.get(fmt.Sprintf("collection_namespaces/%d/", nsID)), 200)
	// The namespace's read role is enough.
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "CollectionNamespace Read"), "user": strangerID, "object_id": nsID}), 201)
	if st, _, raw := galaxyGet(t, spat, path); st != 200 {
		t.Fatalf("stranger with the read role: %d %s", st, raw)
	}

	// Sync tokens read their organization's namespaces only, until revoked
	// or expired.
	tok, revoke, err := resources.MintHubToken(t.Context(), app, org, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st, _, raw := galaxyGet(t, "Token "+tok, path); st != 200 {
		t.Fatalf("sync token: %d %s", st, raw)
	}
	revoke()
	if st, _, _ := galaxyGet(t, "Token "+tok, path); st != 401 {
		t.Fatalf("revoked sync token: %d", st)
	}
	other, revokeOther, err := resources.MintHubToken(t.Context(), app, newOrg(t, a), 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeOther()
	if st, _, _ := galaxyGet(t, "Bearer "+other, path); st != 404 {
		t.Fatalf("another organization's sync token: %d", st)
	}
	expired, _, err := resources.MintHubToken(t.Context(), app, org, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st, _, _ := galaxyGet(t, "Token "+expired, path); st != 401 {
		t.Fatalf("expired sync token: %d", st)
	}
}

// ansibleGalaxy runs ansible-galaxy with a configuration and a home of its
// own, against one server.
type ansibleGalaxy struct {
	t   *testing.T
	dir string
	env []string
}

func newAnsibleGalaxy(t *testing.T, serverURL, token string) *ansibleGalaxy {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "ansible.cfg")
	ini := fmt.Sprintf("[galaxy]\nserver_list = hub\n\n[galaxy_server.hub]\nurl = %s\ntoken = %s\n", serverURL, token)
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	// gpg, when ansible-galaxy checks signatures, never reaches ~/.gnupg.
	gnupg := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(gnupg, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GNUPGHOME="+gnupg, "ANSIBLE_CONFIG="+cfg, "ANSIBLE_HOME="+filepath.Join(dir, "home"),
		"ANSIBLE_GALAXY_CACHE_DIR="+filepath.Join(dir, "cache"), "ANSIBLE_GALAXY_TOKEN_PATH="+filepath.Join(dir, "galaxy_token"),
		"ANSIBLE_COLLECTIONS_PATH="+filepath.Join(dir, "collections"), "ANSIBLE_LOCAL_TEMP="+filepath.Join(dir, "tmp"),
		"ANSIBLE_GALAXY_DISPLAY_PROGRESS=false")
	return &ansibleGalaxy{t: t, dir: dir, env: env}
}

func (g *ansibleGalaxy) run(args ...string) (string, error) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(g.t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ansible-galaxy", args...)
	cmd.Env, cmd.Dir = g.env, g.dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (g *ansibleGalaxy) mustRun(args ...string) string {
	g.t.Helper()
	out, err := g.run(args...)
	if err != nil {
		g.t.Fatalf("ansible-galaxy %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// installedVersion reads the version of an installed collection.
func installedVersion(t *testing.T, root, ns, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "ansible_collections", ns, name, "MANIFEST.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Info struct {
			Version string `json:"version"`
		} `json:"collection_info"`
	}
	json.Unmarshal(b, &m)
	return m.Info.Version
}

func TestHubAnsibleGalaxy(t *testing.T) {
	requireAnsible(t)
	if _, err := exec.LookPath("ansible-galaxy"); err != nil {
		t.Skip("ansible-galaxy not installed")
	}
	a := adminClient(t)
	org := newOrg(t, a)
	member, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": uid, "object_id": org}), 201)
	ns := galaxyName("ns")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		hubPublish(t, collectionTarball(t, ns, "base", v, nil, map[string]string{"plugins/modules/base.py": "# base " + v + "\n"}))
	}
	hubPublish(t, collectionTarball(t, ns, "app", "1.0.0", map[string]string{ns + ".base": ">=1.0.0,<2.0.0"},
		map[string]string{"roles/hello/tasks/main.yml": "- ansible.builtin.debug:\n    msg: hello\n"}))
	pat := patOf(t, member)

	g := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/published/", pat)
	// The dependency resolves in its range, from the versions the hub lists.
	dest := filepath.Join(g.dir, "installed")
	g.mustRun("collection", "install", ns+".app", "-p", dest)
	if a, b := installedVersion(t, dest, ns, "app"), installedVersion(t, dest, ns, "base"); a != "1.0.0" || b != "1.1.0" {
		t.Fatalf("installed app %q and base %q", a, b)
	}
	if _, err := os.Stat(filepath.Join(dest, "ansible_collections", ns, "app", "roles", "hello", "tasks", "main.yml")); err != nil {
		t.Fatalf("installed files: %v", err)
	}
	ranged := filepath.Join(g.dir, "ranged")
	g.mustRun("collection", "install", ns+".base:>=1.0.0,<1.1.0", "-p", ranged)
	if v := installedVersion(t, ranged, ns, "base"); v != "1.0.0" {
		t.Fatalf("installed base %q in its range", v)
	}
	out := g.mustRun("collection", "list", "-p", dest, "--format", "json")
	if !strings.Contains(out, ns+".base") || !strings.Contains(out, "1.1.0") {
		t.Fatalf("collection list:\n%s", out)
	}

	// Download, from the root of the default repository.
	d := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/", pat)
	dl := filepath.Join(d.dir, "download")
	d.mustRun("collection", "download", ns+".app", "-p", dl)
	for _, f := range []string{ns + "-app-1.0.0.tar.gz", ns + "-base-1.1.0.tar.gz", "requirements.yml"} {
		if _, err := os.Stat(filepath.Join(dl, f)); err != nil {
			t.Fatalf("download: %v", err)
		}
	}

	// A user of another organization doesn't find it.
	stranger, _ := newUser(t, a)
	s := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/published/", patOf(t, stranger))
	if out, err := s.run("collection", "install", ns+".app", "-p", filepath.Join(s.dir, "installed")); err == nil || installedVersion(t, filepath.Join(s.dir, "installed"), ns, "app") != "" {
		t.Fatalf("a stranger installed it:\n%s", out)
	}
}

func TestHubProjectRequirements(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	ns := galaxyName("ns")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": f.org}), 201)
	hubPublish(t, collectionTarball(t, ns, "tools", "1.0.0", nil,
		map[string]string{"roles/hello/tasks/main.yml": "- ansible.builtin.debug:\n    msg: from-hub-role\n"}))
	cred := must(t, a.get("credentials/?managed=true&name="+url.QueryEscape(resources.HubCredentialName)), 200)
	if cred.count() != 1 {
		t.Fatalf("hub credential: %s", cred.Raw)
	}
	hub := cred.results()[0]
	if hub["inputs"].(map[string]any)["url"] != resources.HubCredentialPath {
		t.Fatalf("hub credential: %v", hub)
	}
	must(t, a.post(fmt.Sprintf("organizations/%d/galaxy_credentials/", f.org), map[string]any{"id": hub["id"]}), 204)
	cache := t.TempDir()
	prev := a.get("settings/jobs/").Body["GALAXY_TASK_ENV"]
	must(t, a.patch("settings/jobs/", map[string]any{"GALAXY_TASK_ENV": map[string]any{"ANSIBLE_GALAXY_CACHE_DIR": cache}}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"GALAXY_TASK_ENV": prev}) })

	repo := gitRepo(t, map[string]string{
		"collections/requirements.yml": fmt.Sprintf("collections:\n  - name: %s.tools\n    version: '>=1.0.0'\n", ns),
		"site.yml":                     "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.include_role:\n        name: " + ns + ".tools.hello\n",
	})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("hubreqs"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	jt := f.template(t, "site.yml", map[string]any{"limit": "h1"})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	out := f.stdout(t, job.id())
	if !strings.Contains(out, "Installing project requirements") || !strings.Contains(out, "from-hub-role") {
		t.Fatalf("stdout:\n%s", out)
	}
	// The install's token is gone with it, and no credential keeps one.
	var tokens int
	app.DB.QueryRow(t.Context(), "SELECT count(*) FROM hub_tokens WHERE unified_job_id = $1", job.id()).Scan(&tokens)
	if tokens != 0 {
		t.Fatalf("%d hub tokens left after the job", tokens)
	}
	var inputs map[string]any
	app.DB.QueryRow(t.Context(), "SELECT inputs FROM credentials WHERE id = $1", hub["id"]).Scan(&inputs)
	if _, ok := inputs["token"]; ok || strings.Contains(out, resources.HubTokenPrefix) {
		t.Fatalf("a hub token was kept: %v", inputs)
	}
}

package test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/resources"
)

func writePAT(t *testing.T, c *client) string {
	t.Helper()
	return must(t, c.post("tokens/", map[string]any{"description": "hub", "scope": "write"}), 201).Body["token"].(string)
}

// galaxyCall calls the hub with a JSON body, or none.
func galaxyCall(t *testing.T, method, token, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// galaxyUpload posts an artifact as ansible-galaxy collection publish
// does: its sha256, and the file in base64, in a multipart form.
func galaxyUpload(t *testing.T, token, path string, data []byte, sum string) (int, map[string]any) {
	t.Helper()
	return galaxyPost(t, token, path, data, sum, true)
}

func galaxyPost(t *testing.T, token, path string, data []byte, sum string, inBase64 bool) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if sum != "" {
		mw.WriteField("sha256", sum)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="/home/someone/build/collection.tar.gz"`)
	h.Set("Content-Type", "application/octet-stream")
	if inBase64 {
		h.Set("Content-Transfer-Encoding", "base64")
	}
	fw, _ := mw.CreatePart(h)
	if inBase64 {
		enc := base64.StdEncoding.EncodeToString(data)
		for len(enc) > 76 {
			fmt.Fprintf(fw, "%s\n", enc[:76])
			enc = enc[76:]
		}
		fmt.Fprintf(fw, "%s\n", enc)
	} else {
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// publish uploads an artifact and returns its import, as ansible-galaxy
// polls it.
func publish(t *testing.T, token string, data []byte) map[string]any {
	t.Helper()
	status, body := galaxyUpload(t, token, "/api/galaxy/v3/artifacts/collections/", data, sha256Hex(data))
	task, _ := body["task"].(string)
	if status != 202 || !strings.HasPrefix(task, "/api/galaxy/v3/imports/collections/") {
		t.Fatalf("upload: %d %v", status, body)
	}
	status, imp := galaxyCall(t, "GET", token, task, nil)
	if status != 200 || imp["finished_at"] == nil {
		t.Fatalf("import: %d %v", status, imp)
	}
	return imp
}

func importFailed(t *testing.T, imp map[string]any, code, text string) {
	t.Helper()
	e, _ := imp["error"].(map[string]any)
	if imp["state"] != "failed" || e["code"] != code || !strings.Contains(fmt.Sprint(e["description"]), text) {
		t.Fatalf("want a failed import (%s, %q): %v", code, text, imp)
	}
	found := false
	for _, m := range imp["messages"].([]any) {
		if msg, _ := m.(map[string]any); msg["level"] == "ERROR" && strings.Contains(fmt.Sprint(msg["message"]), text) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the import log doesn't say %q: %v", text, imp["messages"])
	}
}

// namespaceRole gives a user a role on a namespace.
func namespaceRole(t *testing.T, a *client, role string, uid, ns int64) {
	t.Helper()
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, role), "user": uid, "object_id": ns}), 201)
}

func versionID(t *testing.T, ns, name, version string) int64 {
	t.Helper()
	var id int64
	if err := app.DB.QueryRow(t.Context(), `SELECT v.id FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id WHERE t.name = $1 AND c.name = $2 AND v.version = $3`, ns, name, version).Scan(&id); err != nil {
		t.Fatalf("version %s.%s %s: %v", ns, name, version, err)
	}
	return id
}

func TestHubPublish(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("pub")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201).id()
	user := func(roles ...string) (*client, string, int64) {
		c, uid := newUser(t, a)
		must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": uid, "object_id": org}), 201)
		for _, r := range roles {
			namespaceRole(t, a, r, uid, nsID)
		}
		return c, writePAT(t, c), uid
	}
	uploaderC, uploader, _ := user("CollectionNamespace Upload")
	_, reader, _ := user()
	ownerC, owner, _ := user("CollectionNamespace Admin")
	strangerC, _ := newUser(t, a)
	stranger := writePAT(t, strangerC)
	// The approver holds Approve through a team.
	approverC, approverID := newUser(t, a)
	approver := writePAT(t, approverC)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("approvers"), "organization": org}), 201).id()
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Team Member"), "user": approverID, "object_id": team}), 201)
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "CollectionNamespace Approve"), "team": team, "object_id": nsID}), 201)

	tools1 := collectionTarball(t, ns, "tools", "1.0.0", nil, nil)
	// Who may not upload: the import says why.
	importFailed(t, publish(t, stranger, tools1), "namespace_not_found", fmt.Sprintf("Namespace %q does not exist", ns))
	importFailed(t, publish(t, reader, tools1), "permission_denied", "You may not upload to namespace "+ns)
	importFailed(t, publish(t, approver, tools1), "permission_denied", "You may not upload to namespace "+ns)
	if status, body := galaxyUpload(t, patOf(t, uploaderC), "/api/galaxy/v3/artifacts/collections/", tools1, ""); status != 403 {
		t.Fatalf("a read-scoped token uploaded: %d %v", status, body)
	}
	if status, _ := galaxyUpload(t, "", "/api/galaxy/v3/artifacts/collections/", tools1, ""); status != 401 {
		t.Fatalf("anonymous upload: %d", status)
	}

	imp := publish(t, uploader, tools1)
	if imp["state"] != "completed" || imp["namespace"] != ns || imp["name"] != "tools" || imp["version"] != "1.0.0" {
		t.Fatalf("import: %v", imp)
	}
	var log []string
	for _, m := range imp["messages"].([]any) {
		msg := m.(map[string]any)
		log = append(log, fmt.Sprint(msg["level"], " ", msg["message"]))
	}
	for _, want := range []string{"SHA-256: ", "Layout: ", "Checksums: ", "Metadata: " + ns + ".tools 1.0.0", "Namespace: " + ns, "Staged " + ns + ".tools 1.0.0"} {
		if !strings.Contains(strings.Join(log, "\n"), want) {
			t.Fatalf("the import log lacks %q:\n%s", want, strings.Join(log, "\n"))
		}
	}
	// The import is the uploader's and the namespace's curators', no one else's.
	task := fmt.Sprintf("/api/galaxy/v3/imports/collections/%v/", imp["id"])
	for tok, want := range map[string]int{approver: 200, owner: 200, reader: 404, stranger: 404} {
		if status, _ := galaxyCall(t, "GET", tok, task, nil); status != want {
			t.Fatalf("import read: %d, want %d", status, want)
		}
	}
	if status, _ := galaxyCall(t, "GET", uploader, fmt.Sprintf("/api/galaxy/content/staging/v3/imports/collections/%v/", imp["id"]), nil); status != 200 {
		t.Fatalf("import under content/staging/: %d", status)
	}

	// Staged: not installable; its curators see it in staging, readers don't.
	published := "/api/galaxy/v3/plugin/ansible/content/published/collections/index/" + ns + "/tools/"
	staged := "/api/galaxy/v3/plugin/ansible/content/staging/collections/index/" + ns + "/tools/versions/1.0.0/"
	if status, _ := galaxyCall(t, "GET", reader, published, nil); status != 404 {
		t.Fatalf("a staged collection is published: %d", status)
	}
	for tok, want := range map[string]int{uploader: 200, approver: 200, owner: 200, reader: 404, stranger: 404} {
		if status, _ := galaxyCall(t, "GET", tok, staged, nil); status != want {
			t.Fatalf("staged version read: %d, want %d", status, want)
		}
	}
	state0, err := resources.HubPublishedState(t.Context(), app, org)
	if err != nil {
		t.Fatal(err)
	}

	// Approval: the uploader may not, the approver may.
	move := "/api/galaxy/v3/collections/" + ns + "/tools/versions/1.0.0/move/staging/published/"
	if status, body := galaxyCall(t, "POST", uploader, move, nil); status != 403 || !strings.Contains(fmt.Sprint(body), "may not approve") {
		t.Fatalf("the uploader approved: %d %v", status, body)
	}
	if status, _ := galaxyCall(t, "POST", reader, move, nil); status != 404 {
		t.Fatalf("a reader found the staged version: %d", status)
	}
	if status, body := galaxyCall(t, "POST", approver, move, nil); status != 202 {
		t.Fatalf("approve: %d %v", status, body)
	}
	if status, body := galaxyCall(t, "POST", approver, move, nil); status != 404 {
		t.Fatalf("approved twice: %d %v", status, body)
	}
	if status, body := galaxyCall(t, "GET", reader, published, nil); status != 200 || body["highest_version"].(map[string]any)["version"] != "1.0.0" {
		t.Fatalf("approved, not published: %d %v", status, body)
	}
	if state1, _ := resources.HubPublishedState(t.Context(), app, org); state1 == state0 {
		t.Fatal("the requirements cache doesn't see an approval")
	}
	v1 := must(t, a.get(fmt.Sprintf("collection_versions/%d/", versionID(t, ns, "tools", "1.0.0"))), 200).Body
	if sf := v1["summary_fields"].(map[string]any); fmt.Sprint(sf["repositories"]) != "[published]" || sf["reviewed_by"].(map[string]any)["id"] != float64(approverID) {
		t.Fatalf("approved version: %v", v1)
	}

	// A version is uploaded once; what changed on the way is refused.
	importFailed(t, publish(t, uploader, tools1), "duplicate_version", ns+".tools 1.0.0 already exists")
	tools2 := collectionTarball(t, ns, "tools", "1.1.0", nil, nil)
	status, body := galaxyUpload(t, uploader, "/api/galaxy/v3/artifacts/collections/", tools2, strings.Repeat("0", 64))
	if status != 202 {
		t.Fatalf("upload: %d %v", status, body)
	}
	_, imp = galaxyCall(t, "GET", uploader, body["task"].(string), nil)
	importFailed(t, imp, "checksum_mismatch", "it changed on the way")
	tampered := rewriteMember(t, tools2, "README.md", "# changed\n")
	importFailed(t, publish(t, uploader, tampered), "checksum_mismatch", "README.md: its SHA-256 is")
	evil := rewriteMember(t, tools2, "../escape", "x")
	importFailed(t, publish(t, uploader, evil), "invalid_artifact", "outside the collection")

	// The upload limit.
	prev := a.get("settings/system/").Body["HUB_MAX_UPLOAD_SIZE"]
	must(t, a.patch("settings/system/", map[string]any{"HUB_MAX_UPLOAD_SIZE": 1024}), 200)
	status, body = galaxyUpload(t, uploader, "/api/galaxy/v3/artifacts/collections/", append(tools2, make([]byte, 4096)...), "")
	must(t, a.patch("settings/system/", map[string]any{"HUB_MAX_UPLOAD_SIZE": prev}), 200)
	if status != 413 || !strings.Contains(fmt.Sprint(body), "HUB_MAX_UPLOAD_SIZE") {
		t.Fatalf("over the limit: %d %v", status, body)
	}

	// Rejection, on /api/v2/: the version leaves staging for rejected. The
	// file comes as plain bytes, as other clients send it.
	status, body = galaxyPost(t, uploader, "/api/galaxy/content/published/v3/artifacts/collections/", tools2, sha256Hex(tools2), false)
	if status != 202 {
		t.Fatalf("upload 1.1.0: %d %v", status, body)
	}
	if _, imp := galaxyCall(t, "GET", uploader, body["task"].(string), nil); imp["state"] != "completed" {
		t.Fatalf("upload 1.1.0: %v", imp)
	}
	id2 := versionID(t, ns, "tools", "1.1.0")
	must(t, uploaderC.post(fmt.Sprintf("collection_versions/%d/reject/", id2), nil), 403)
	rejected := must(t, approverC.post(fmt.Sprintf("collection_versions/%d/reject/", id2), nil), 200).Body
	if fmt.Sprint(rejected["summary_fields"].(map[string]any)["repositories"]) != "[rejected]" {
		t.Fatalf("rejected: %v", rejected)
	}
	if status, _ := galaxyCall(t, "GET", reader, "/api/galaxy/v3/collections/"+ns+"/tools/versions/1.1.0/", nil); status != 404 {
		t.Fatalf("a rejected version is installable: %d", status)
	}
	if status, _ := galaxyCall(t, "GET", uploader, "/api/galaxy/content/rejected/v3/collections/"+ns+"/tools/versions/1.1.0/", nil); status != 200 {
		t.Fatalf("the uploader doesn't see its rejected version: %d", status)
	}

	// On /api/v2/: readers see published versions, curators the rest.
	list := func(c *client, q string) []string {
		var out []string
		for _, v := range must(t, c.get(fmt.Sprintf("collection_versions/?namespace=%d&%s", nsID, q)), 200).results() {
			out = append(out, v["version"].(string))
		}
		return out
	}
	if got := fmt.Sprint(list(uploaderC, "order_by=version")); got != "[1.0.0 1.1.0]" {
		t.Fatalf("uploader's versions: %s", got)
	}
	if got := fmt.Sprint(list(uploaderC, "repository=rejected")); got != "[1.1.0]" {
		t.Fatalf("rejected versions: %s", got)
	}
	if got := fmt.Sprint(list(strangerC, "")); got != "[]" {
		t.Fatalf("stranger's versions: %s", got)
	}
	imports := must(t, approverC.get(fmt.Sprintf("collection_imports/?namespace=%d", nsID)), 200)
	if imports.count() < 3 {
		t.Fatalf("the approver's imports: %s", imports.Raw)
	}
	if n := must(t, strangerC.get(fmt.Sprintf("collection_imports/?namespace=%d", nsID)), 200).count(); n != 0 {
		t.Fatalf("a stranger sees %d imports", n)
	}
	if mine := must(t, strangerC.get("collection_imports/"), 200); mine.count() != 1 || mine.results()[0]["namespace"] != nil {
		t.Fatalf("the stranger's own import: %s", mine.Raw)
	}

	// Auto approval publishes at once.
	must(t, uploaderC.patch(fmt.Sprintf("collection_namespaces/%d/", nsID), map[string]any{"auto_approve": true}), 403)
	must(t, ownerC.patch(fmt.Sprintf("collection_namespaces/%d/", nsID), map[string]any{"auto_approve": true}), 200)
	imp = publish(t, uploader, collectionTarball(t, ns, "tools", "2.0.0", nil, nil))
	if !strings.Contains(fmt.Sprint(imp["messages"]), "Published "+ns+".tools 2.0.0") {
		t.Fatalf("auto approval: %v", imp)
	}
	if _, body := galaxyCall(t, "GET", reader, published, nil); body["highest_version"].(map[string]any)["version"] != "2.0.0" {
		t.Fatalf("auto approved, not published: %v", body)
	}

	// Deprecation is the owner's.
	if status, _ := galaxyCall(t, "PATCH", uploader, published, map[string]any{"deprecated": true}); status != 403 {
		t.Fatalf("the uploader deprecated: %d", status)
	}
	if status, body := galaxyCall(t, "PATCH", owner, published, map[string]any{"deprecated": true}); status != 200 || body["deprecated"] != true {
		t.Fatalf("deprecate: %d %v", status, body)
	}
	if _, body := galaxyCall(t, "GET", reader, published, nil); body["deprecated"] != true {
		t.Fatalf("not deprecated: %v", body)
	}
	coll, _ := api.ID(v1["collection"])
	must(t, ownerC.patch(fmt.Sprintf("collections/%d/", coll), map[string]any{"deprecated": false}), 200)

	// Deletion: the owner's, and a rejected version goes freely.
	must(t, uploaderC.delete(fmt.Sprintf("collection_versions/%d/", id2)), 403)
	must(t, ownerC.delete(fmt.Sprintf("collection_versions/%d/", id2)), 204)
	if status, _ := galaxyCall(t, "DELETE", uploader, "/api/galaxy/v3/collections/"+ns+"/tools/versions/2.0.0/", nil); status != 403 {
		t.Fatalf("the uploader deleted: %d", status)
	}
	if status, _ := galaxyCall(t, "DELETE", owner, "/api/galaxy/v3/collections/"+ns+"/tools/versions/2.0.0/", nil); status != 204 {
		t.Fatalf("delete: %d", status)
	}
	if _, body := galaxyCall(t, "GET", reader, published, nil); body["highest_version"].(map[string]any)["version"] != "1.0.0" {
		t.Fatalf("after deleting 2.0.0: %v", body)
	}
	if status, _ := galaxyCall(t, "DELETE", owner, published, nil); status != 204 {
		t.Fatalf("delete the collection: %d", status)
	}
	if status, _ := galaxyCall(t, "GET", reader, published, nil); status != 404 {
		t.Fatalf("a deleted collection: %d", status)
	}
	must(t, a.get(fmt.Sprintf("collections/%d/", coll)), 404)
}

// rewriteMember replaces a member of a tarball, or adds it, without
// touching MANIFEST.json and FILES.json.
func rewriteMember(t *testing.T, data []byte, name, content string) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Name == name {
			b, found = []byte(content), true
			h.Size = int64(len(b))
		}
		tw.WriteHeader(h)
		tw.Write(b)
	}
	if !found {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestHubPublishUsedBy(t *testing.T) {
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	ns := galaxyName("dep")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201).id()
	owner, uid := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Admin", uid, nsID)
	tok := writePAT(t, owner)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if imp := publish(t, tok, collectionTarball(t, ns, "base", v, nil, nil)); imp["state"] != "completed" {
			t.Fatalf("upload: %v", imp)
		}
	}
	publish(t, tok, collectionTarball(t, ns, "app", "1.0.0", map[string]string{ns + ".base": ">=1.1.0"}, nil))
	repo := gitRepo(t, map[string]string{
		"site.yml":                     helloPlaybook,
		"collections/requirements.yml": fmt.Sprintf("collections:\n  - name: %s.base\n    version: '==1.0.0'\n  - name: %s.app\n", ns, ns),
	})
	project := must(t, a.post("projects/", map[string]any{"name": uniq("hubdeps"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()
	waitProjectSynced(t, a, project, "")
	reqs := must(t, a.get(fmt.Sprintf("projects/%d/", project)), 200)
	var stored []map[string]any
	app.DB.QueryRow(t.Context(), "SELECT collection_requirements FROM unified_job_templates WHERE id = $1", project).Scan(&stored)
	if len(stored) != 2 || stored[0]["name"] != ns+".base" || stored[0]["version"] != "==1.0.0" {
		t.Fatalf("project %v requirements: %v", reqs.Body["name"], stored)
	}

	// 1.0.0 is the only version the project's pin meets: it stays.
	v100, v110 := versionID(t, ns, "base", "1.0.0"), versionID(t, ns, "base", "1.1.0")
	used := must(t, a.get(fmt.Sprintf("collection_versions/%d/used_by/", v100)), 200)
	if used.count() != 1 || used.results()[0]["resource"] != "projects" || used.results()[0]["effect"] != "blocks" {
		t.Fatalf("used_by: %s", used.Raw)
	}
	// The owner doesn't see the project, only that it is there.
	if used := must(t, owner.get(fmt.Sprintf("collection_versions/%d/used_by/", v100)), 200); used.count() != 0 || used.Body["hidden"] != float64(1) {
		t.Fatalf("the owner's used_by: %s", used.Raw)
	}
	if r := must(t, owner.delete(fmt.Sprintf("collection_versions/%d/", v100)), 400); !strings.Contains(r.Raw, "can't be deleted") || !strings.Contains(r.Raw, "1 more") {
		t.Fatalf("delete a required version: %s", r.Raw)
	}
	if r := must(t, a.delete(fmt.Sprintf("collection_versions/%d/", v100)), 400); !strings.Contains(r.Raw, "project hubdeps") {
		t.Fatalf("delete a required version: %s", r.Raw)
	}
	// 1.1.0 is what app needs.
	used = must(t, owner.get(fmt.Sprintf("collection_versions/%d/used_by/", v110)), 200)
	if used.count() != 1 || used.results()[0]["resource"] != "collection_versions" || used.results()[0]["requirement"] != ">=1.1.0" {
		t.Fatalf("used_by: %s", used.Raw)
	}
	if status, body := galaxyCall(t, "DELETE", tok, "/api/galaxy/v3/collections/"+ns+"/base/versions/1.1.0/", nil); status != 400 ||
		!strings.Contains(fmt.Sprint(body), ns+".app 1.0.0") {
		t.Fatalf("delete a dependency: %d %v", status, body)
	}
	// A third version meeting the dependency frees 1.1.0.
	publish(t, tok, collectionTarball(t, ns, "base", "1.2.0", nil, nil))
	must(t, owner.delete(fmt.Sprintf("collection_versions/%d/", v110)), 204)
	coll := must(t, owner.get(fmt.Sprintf("collections/?namespace=%d&name=base", nsID)), 200).results()[0]
	if n := must(t, a.get(fmt.Sprintf("collections/%v/used_by/", coll["id"])), 200).count(); n != 2 {
		t.Fatalf("the collection's used_by: %d", n)
	}
}

func TestHubPublishAnsibleGalaxy(t *testing.T) {
	requireAnsible(t)
	if _, err := exec.LookPath("ansible-galaxy"); err != nil {
		t.Skip("ansible-galaxy not installed")
	}
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("cli")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201).id()
	uploaderC, uploaderID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Upload", uploaderID, nsID)
	approverC, approverID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Approve", approverID, nsID)
	readerC, readerID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": readerID, "object_id": org}), 201)

	up := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/", writePAT(t, uploaderC))
	src := filepath.Join(up.dir, "src")
	up.mustRun("collection", "init", ns+".tools", "--init-path", src)
	root := filepath.Join(src, ns, "tools")
	galaxyYML := filepath.Join(root, "galaxy.yml")
	b, err := os.ReadFile(galaxyYML)
	if err != nil {
		t.Fatal(err)
	}
	build := func(version string) string {
		t.Helper()
		yml := strings.Replace(string(b), "version: 1.0.0", "version: "+version, 1)
		if err := os.WriteFile(galaxyYML, []byte(yml), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(up.dir, "dist")
		up.mustRun("collection", "build", root, "--output-path", out, "--force")
		return filepath.Join(out, fmt.Sprintf("%s-tools-%s.tar.gz", ns, version))
	}
	v1 := build("1.0.0")
	out := up.mustRun("collection", "publish", v1)
	if !strings.Contains(out, "completed") && !strings.Contains(out, "Collection has been published") {
		t.Fatalf("publish:\n%s", out)
	}

	reader := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/published/", patOf(t, readerC))
	dest := filepath.Join(reader.dir, "installed")
	if out, err := reader.run("collection", "install", ns+".tools", "-p", dest); err == nil {
		t.Fatalf("installed before approval:\n%s", out)
	}
	must(t, approverC.post(fmt.Sprintf("collection_versions/%d/approve/", versionID(t, ns, "tools", "1.0.0")), nil), 200)
	reader.mustRun("collection", "install", ns+".tools", "-p", dest)
	if v := installedVersion(t, dest, ns, "tools"); v != "1.0.0" {
		t.Fatalf("installed %q", v)
	}

	// The client shows why an upload is refused.
	out, err = up.run("collection", "publish", v1)
	if err == nil || !strings.Contains(out, "already exists") || !strings.Contains(out, "duplicate_version") {
		t.Fatalf("duplicate publish: %v\n%s", err, out)
	}
	v2 := build("1.1.0")
	data, err := os.ReadFile(v2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2, rewriteMember(t, data, "README.md", "# changed after the build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = up.run("collection", "publish", v2)
	if err == nil || !strings.Contains(out, "README.md: its SHA-256 is") || !strings.Contains(out, "checksum_mismatch") {
		t.Fatalf("tampered publish: %v\n%s", err, out)
	}
	ap := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/", writePAT(t, approverC))
	out, err = ap.run("collection", "publish", build("1.2.0"))
	if err == nil || !strings.Contains(out, "You may not upload to namespace "+ns) {
		t.Fatalf("the approver published: %v\n%s", err, out)
	}

	// Rejected: never installable.
	up.mustRun("collection", "publish", build("1.3.0"))
	must(t, approverC.post(fmt.Sprintf("collection_versions/%d/reject/", versionID(t, ns, "tools", "1.3.0")), nil), 200)
	pinned := filepath.Join(reader.dir, "pinned")
	if out, err := reader.run("collection", "install", ns+".tools:1.3.0", "-p", pinned); err == nil {
		t.Fatalf("installed a rejected version:\n%s", out)
	}
}

package test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func collectionNames(r resp) []string {
	var out []string
	for _, c := range r.results() {
		out = append(out, fmt.Sprint(c["name"]))
	}
	return out
}

// TestHubBrowse: the collections page finds collections by namespace, tag,
// repository, signature and words, among what the user sees, with each
// one's latest version.
func TestHubBrowse(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("browse")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201).id()
	owner, ownerID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Admin", ownerID, nsID)
	reader, readerID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": readerID, "object_id": org}), 201)
	tok := writePAT(t, owner)
	pub := func(name, version string, info map[string]any) {
		t.Helper()
		if imp := publish(t, tok, collectionTarballInfo(t, ns, name, version, info, nil)); imp["state"] != "completed" {
			t.Fatalf("upload: %v", imp)
		}
	}
	for _, v := range []string{"1.0.0", "1.10.0", "1.9.0"} {
		pub("netkit", v, map[string]any{"tags": []string{"networking"}, "description": "Switches and routers"})
	}
	pub("cloudkit", "1.0.0", map[string]any{"tags": []string{"cloud"}, "description": "Instances"})
	must(t, a.patch(fmt.Sprintf("collection_namespaces/%d/", nsID), map[string]any{"auto_approve": false}), 200)
	pub("netkit", "2.0.0", map[string]any{"tags": []string{"networking"}})
	pub("stagedkit", "1.0.0", map[string]any{"tags": []string{"networking"}})

	signed := versionID(t, ns, "netkit", "1.10.0")
	app.DB.Exec(t.Context(), "DELETE FROM collection_version_signatures WHERE collection_version_id IN (SELECT v.id FROM collection_versions v JOIN collections c ON c.id = v.collection_id WHERE c.namespace_id = $1)", nsID)
	if _, err := app.DB.Exec(t.Context(), `INSERT INTO collection_version_signatures (collection_version_id, origin, signature, pubkey_fingerprint)
		VALUES ($1, 'upload', 'sig', 'fp')`, signed); err != nil {
		t.Fatal(err)
	}

	list := func(c *client, q url.Values) []string {
		t.Helper()
		q.Set("namespace", fmt.Sprint(nsID))
		q.Set("order_by", "name")
		return collectionNames(must(t, c.get("collections/?"+q.Encode()), 200))
	}
	check := func(c *client, q url.Values, want string) {
		t.Helper()
		if got := strings.Join(list(c, q), ","); got != want {
			t.Errorf("%s: got %q, want %q", q.Encode(), got, want)
		}
	}
	check(reader, url.Values{}, "cloudkit,netkit")
	check(owner, url.Values{}, "cloudkit,netkit,stagedkit")
	check(reader, url.Values{"tag": {"networking"}}, "netkit")
	check(owner, url.Values{"tag": {"networking"}}, "netkit,stagedkit")
	check(reader, url.Values{"repository": {"published"}}, "cloudkit,netkit")
	check(owner, url.Values{"repository": {"staging"}}, "netkit,stagedkit")
	check(reader, url.Values{"repository": {"staging"}}, "")
	check(reader, url.Values{"signed": {"true"}}, "netkit")
	check(reader, url.Values{"signed": {"false"}}, "cloudkit")
	check(reader, url.Values{"search": {"routers"}}, "netkit")
	check(reader, url.Values{"search": {ns + ".cloud"}}, "cloudkit")
	check(reader, url.Values{"search": {"nothing-like-it"}}, "")
	nsCaps := func(c *client) string {
		t.Helper()
		caps := must(t, c.get(fmt.Sprintf("collection_namespaces/%d/", nsID)), 200).Body["summary_fields"].(map[string]any)["user_capabilities"].(map[string]any)
		return fmt.Sprintf("edit=%v upload=%v approve=%v", caps["edit"], caps["upload"], caps["approve"])
	}
	if got := nsCaps(owner); got != "edit=true upload=true approve=true" {
		t.Errorf("the owner's capabilities on the namespace: %s", got)
	}
	if got := nsCaps(reader); got != "edit=false upload=false approve=false" {
		t.Errorf("the reader's capabilities on the namespace: %s", got)
	}
	if r := reader.get("collections/?signed=maybe"); r.Status != 400 {
		t.Errorf("signed=maybe: %d", r.Status)
	}

	latest := func(c *client) map[string]any {
		t.Helper()
		r := must(t, c.get(fmt.Sprintf("collections/?namespace=%d&name=netkit", nsID)), 200).results()[0]
		sf := r["summary_fields"].(map[string]any)
		if n := sf["namespace"].(map[string]any)["name"]; n != ns {
			t.Errorf("namespace summary: %v", sf["namespace"])
		}
		return sf
	}
	if sf := latest(reader); sf["version_count"] != float64(3) || sf["latest_version"].(map[string]any)["version"] != "1.10.0" ||
		sf["latest_version"].(map[string]any)["signed"] != true || sf["latest_version"].(map[string]any)["description"] != "Switches and routers" {
		t.Errorf("the reader's latest version: %v", sf)
	}
	if sf := latest(owner); sf["version_count"] != float64(4) || sf["latest_version"].(map[string]any)["version"] != "2.0.0" ||
		fmt.Sprint(sf["latest_version"].(map[string]any)["repositories"]) != "[staging]" {
		t.Errorf("the owner's latest version: %v", sf)
	}
}

// TestHubVersionDiff: a version's files against the previous version's,
// for its review.
func TestHubVersionDiff(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("diff")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201)
	tok := writePAT(t, a)
	for v, files := range map[string]map[string]string{
		"1.0.0": {"plugins/modules/m.py": "a\nb\nc\n", "docs/x.md": "x\n", "bin.dat": "\x00\x01"},
		"1.1.0": {"plugins/modules/m.py": "a\nB\nc\n", "new.txt": "n\n", "bin.dat": "\x00\x02"},
	} {
		if imp := publish(t, tok, collectionTarball(t, ns, "kit", v, nil, files)); imp["state"] != "completed" {
			t.Fatalf("upload: %v", imp)
		}
	}
	v100, v110 := versionID(t, ns, "kit", "1.0.0"), versionID(t, ns, "kit", "1.1.0")
	d := must(t, a.get(fmt.Sprintf("collection_versions/%d/diff/", v110)), 200)
	if against := d.Body["against"].(map[string]any); against["version"] != "1.0.0" || against["id"] != float64(v100) {
		t.Fatalf("against: %v", d.Body["against"])
	}
	files := map[string]map[string]any{}
	for _, f := range d.Body["files"].([]any) {
		f := f.(map[string]any)
		files[f["name"].(string)] = f
	}
	if len(files) != 4 {
		t.Fatalf("files: %s", d.Raw)
	}
	if f := files["new.txt"]; f["status"] != "added" || f["diff"] != "@@ -0,0 +1 @@\n+n\n" {
		t.Errorf("new.txt: %v", f)
	}
	if f := files["docs/x.md"]; f["status"] != "removed" || f["diff"] != "@@ -1 +0,0 @@\n-x\n" {
		t.Errorf("docs/x.md: %v", f)
	}
	if f := files["plugins/modules/m.py"]; f["status"] != "changed" || !strings.Contains(fmt.Sprint(f["diff"]), "-b\n+B\n") {
		t.Errorf("m.py: %v", f)
	}
	if f := files["bin.dat"]; f["status"] != "changed" || f["omitted"] != "binary" || f["diff"] != nil {
		t.Errorf("bin.dat: %v", f)
	}
	// The first version: everything was added.
	first := must(t, a.get(fmt.Sprintf("collection_versions/%d/diff/", v100)), 200)
	if first.Body["against"] != nil || first.Body["counts"].(map[string]any)["added"] != float64(5) {
		t.Errorf("the first version's diff: %s", first.Raw)
	}
	// Against a later version, and against another collection's.
	back := must(t, a.get(fmt.Sprintf("collection_versions/%d/diff/?against=%d", v100, v110)), 200)
	if back.Body["counts"].(map[string]any)["added"] != float64(1) || back.Body["counts"].(map[string]any)["removed"] != float64(1) {
		t.Errorf("1.0.0 against 1.1.0: %s", back.Raw)
	}
	publish(t, tok, collectionTarball(t, ns, "other", "1.0.0", nil, nil))
	must(t, a.get(fmt.Sprintf("collection_versions/%d/diff/?against=%d", v110, versionID(t, ns, "other", "1.0.0"))), 400)
}

// TestHubUsedByExecutionEnvironment: a collection is used by the execution
// environments whose images hold it (M7-07), a version by those holding
// that version.
func TestHubUsedByExecutionEnvironment(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("eeuse")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201).id()
	tok := writePAT(t, a)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		publish(t, tok, collectionTarball(t, ns, "kit", v, nil, nil))
	}
	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("hubee"), "image": "registry.example.com/ee:1", "organization": org}), 201).id()
	if _, err := app.DB.Exec(t.Context(), `INSERT INTO execution_environment_contents (execution_environment_id, image, status, contents, finished)
		VALUES ($1, 'registry.example.com/ee:1', 'successful', $2, now())`, ee,
		fmt.Sprintf(`{"collections": [{"name": "%s.kit", "version": "1.0.0", "path": "/usr/share/ansible/collections"}]}`, ns)); err != nil {
		t.Fatal(err)
	}
	used := must(t, a.get(fmt.Sprintf("collection_versions/%d/used_by/", versionID(t, ns, "kit", "1.0.0"))), 200)
	if used.count() != 1 || used.results()[0]["type"] != "execution_environment" || used.results()[0]["id"] != float64(ee) ||
		used.results()[0]["requirement"] != "1.0.0" || used.results()[0]["effect"] != "none" {
		t.Fatalf("used_by: %s", used.Raw)
	}
	if n := must(t, a.get(fmt.Sprintf("collection_versions/%d/used_by/", versionID(t, ns, "kit", "1.1.0"))), 200).count(); n != 0 {
		t.Errorf("1.1.0's used_by: %d", n)
	}
	coll := must(t, a.get(fmt.Sprintf("collections/?namespace=%d&name=kit", nsID)), 200).results()[0]
	if n := must(t, a.get(fmt.Sprintf("collections/%v/used_by/", coll["id"])), 200).count(); n != 1 {
		t.Errorf("the collection's used_by: %d", n)
	}
	// A reader of the namespace who doesn't see the environment.
	user, uid := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Read", uid, nsID)
	if u := must(t, user.get(fmt.Sprintf("collections/%v/used_by/", coll["id"])), 200); u.count() != 0 || u.Body["hidden"] != float64(1) {
		t.Errorf("the reader's used_by: %s", u.Raw)
	}
	// Deleting the version isn't refused for it.
	must(t, a.delete(fmt.Sprintf("collection_versions/%d/", versionID(t, ns, "kit", "1.0.0"))), 204)
}

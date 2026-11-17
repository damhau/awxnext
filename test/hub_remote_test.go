package test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/hubsign"
	"github.com/damhau/awxnext/internal/resources"
)

// fakeGalaxy serves collections on Galaxy's v3 API as galaxy_ng does: the
// API root under /api/, two versions a page with relative links, version
// details with their dependencies and signatures, and artifacts. With
// offline set it wants Red Hat's SSO access tokens, which expire after
// maxUses requests; else the token, when set.
type fakeGalaxy struct {
	*httptest.Server
	mu        sync.Mutex
	versions  map[string][]*fakeVersion
	token     string
	offline   string
	access    string
	uses      int
	maxUses   int
	refreshes int
}

type fakeVersion struct {
	ns, name, version string
	deps              map[string]string
	data              []byte
	sigs              []string
}

func (v *fakeVersion) file() string { return fmt.Sprintf("%s-%s-%s.tar.gz", v.ns, v.name, v.version) }

func newFakeGalaxy(t *testing.T) *fakeGalaxy {
	g := &fakeGalaxy{versions: map[string][]*fakeVersion{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/token", g.ssoToken)
	mux.HandleFunc("GET /api/{$}", g.authed(func(w http.ResponseWriter, r *http.Request) {
		writeFakeJSON(w, 200, map[string]any{"available_versions": map[string]any{"v3": "v3/", "pulp-v3": "pulp/api/v3/"}})
	}))
	mux.HandleFunc("GET /api/v3/collections/{ns}/{name}/{$}", g.authed(g.collection))
	mux.HandleFunc("GET /api/v3/collections/{ns}/{name}/versions/{$}", g.authed(g.list))
	mux.HandleFunc("GET /api/v3/collections/{ns}/{name}/versions/{version}/{$}", g.authed(g.detail))
	mux.HandleFunc("GET /api/v3/plugin/ansible/content/published/collections/artifacts/{file}", g.authed(g.artifact))
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func writeFakeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// add publishes a version upstream, signed with each key.
func (g *fakeGalaxy) add(t *testing.T, ns, name, version string, deps map[string]string, keys ...*hubsign.Key) *fakeVersion {
	t.Helper()
	v := &fakeVersion{ns: ns, name: name, version: version, deps: deps,
		data: collectionTarball(t, ns, name, version, deps, map[string]string{"plugins/modules/" + name + ".py": "# " + version + "\n"})}
	for _, k := range keys {
		sig, err := hubsign.Sign(k.PrivateKey, artifactMember(t, v.data, "MANIFEST.json"))
		if err != nil {
			t.Fatal(err)
		}
		v.sigs = append(v.sigs, sig)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.versions[ns+"."+name] = append(g.versions[ns+"."+name], v)
	return v
}

func (g *fakeGalaxy) ssoToken(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "cloud-services" || g.offline == "" || r.Form.Get("refresh_token") != g.offline {
		writeFakeJSON(w, 400, map[string]any{"error": "invalid_grant"})
		return
	}
	g.refreshes++
	g.access, g.uses = fmt.Sprintf("access-%d", g.refreshes), 0
	writeFakeJSON(w, 200, map[string]any{"access_token": g.access, "expires_in": 300, "token_type": "Bearer"})
}

func (g *fakeGalaxy) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		ok := true
		switch {
		case g.offline != "":
			ok = r.Header.Get("Authorization") == "Bearer "+g.access && g.access != "" && (g.maxUses == 0 || g.uses < g.maxUses)
			g.uses++
		case g.token != "":
			ok = r.Header.Get("Authorization") == "Token "+g.token
		}
		g.mu.Unlock()
		if !ok {
			writeFakeJSON(w, 401, map[string]any{"errors": []any{map[string]any{"status": "401", "code": "not_authenticated", "title": "Authentication credentials were not provided."}}})
			return
		}
		h(w, r)
	}
}

func (g *fakeGalaxy) find(r *http.Request) []*fakeVersion {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.versions[r.PathValue("ns")+"."+r.PathValue("name")])
}

func notFound(w http.ResponseWriter) {
	writeFakeJSON(w, 404, map[string]any{"errors": []any{map[string]any{"status": "404", "code": "not_found", "title": "Not found."}}})
}

func (g *fakeGalaxy) collection(w http.ResponseWriter, r *http.Request) {
	list := g.find(r)
	if len(list) == 0 {
		notFound(w)
		return
	}
	writeFakeJSON(w, 200, map[string]any{"namespace": list[0].ns, "name": list[0].name, "created_at": "2026-01-01T00:00:00Z",
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano), "highest_version": map[string]any{"version": list[len(list)-1].version}})
}

func (g *fakeGalaxy) list(w http.ResponseWriter, r *http.Request) {
	list := g.find(r)
	if len(list) == 0 {
		notFound(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit = min(max(limit, 1), 2)
	data := []any{}
	for i := offset; i < len(list) && i < offset+limit; i++ {
		data = append(data, map[string]any{"version": list[i].version, "href": r.URL.Path + list[i].version + "/", "marks": []any{}})
	}
	var next any
	if offset+limit < len(list) {
		next = fmt.Sprintf("%s?limit=%d&offset=%d", r.URL.Path, limit, offset+limit)
	}
	writeFakeJSON(w, 200, map[string]any{"meta": map[string]any{"count": len(list)}, "links": map[string]any{"next": next}, "data": data})
}

func (g *fakeGalaxy) detail(w http.ResponseWriter, r *http.Request) {
	for _, v := range g.find(r) {
		if v.version != r.PathValue("version") {
			continue
		}
		sum := sha256.Sum256(v.data)
		sigs := []any{}
		for _, s := range v.sigs {
			_, fp, _ := hubsign.Inspect(s)
			sigs = append(sigs, map[string]any{"signature": s, "pubkey_fingerprint": strings.ToLower(fp), "signing_service": "ansible-default"})
		}
		deps := map[string]any{}
		for k, d := range v.deps {
			deps[k] = d
		}
		writeFakeJSON(w, 200, map[string]any{
			"namespace": map[string]any{"name": v.ns}, "collection": map[string]any{"name": v.name}, "name": v.name, "version": v.version,
			"href":         r.URL.Path,
			"download_url": g.URL + "/api/v3/plugin/ansible/content/published/collections/artifacts/" + v.file(),
			"artifact":     map[string]any{"filename": v.file(), "sha256": hex.EncodeToString(sum[:]), "size": len(v.data)},
			"metadata":     map[string]any{"dependencies": deps},
			"signatures":   sigs,
		})
		return
	}
	notFound(w)
}

func (g *fakeGalaxy) artifact(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, list := range g.versions {
		for _, v := range list {
			if v.file() == r.PathValue("file") {
				w.Header().Set("Content-Type", "application/gzip")
				w.Write(v.data)
				return
			}
		}
	}
	notFound(w)
}

// replace swaps a version's artifact upstream, as a server that rebuilt it.
func (g *fakeGalaxy) replace(t *testing.T, v *fakeVersion) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	v.data = collectionTarball(t, v.ns, v.name, v.version, v.deps, map[string]string{"plugins/modules/" + v.name + ".py": "# rebuilt\n"})
	v.sigs = nil
}

// repoVersions lists a collection's versions in a hub repository, as
// ansible-galaxy reads them; nil when the caller doesn't find it.
func repoVersions(t *testing.T, token, repo, ns, name string) []string {
	t.Helper()
	status, body, raw := galaxyGet(t, "Token "+token, fmt.Sprintf("/api/galaxy/content/%s/v3/collections/%s/%s/versions/?limit=100", repo, ns, name))
	if status == 404 {
		return nil
	}
	if status != 200 {
		t.Fatalf("versions of %s.%s in %s: %d %s", ns, name, repo, status, raw)
	}
	var out []string
	for _, d := range body["data"].([]any) {
		out = append(out, d.(map[string]any)["version"].(string))
	}
	sort.Strings(out)
	return out
}

func repoSignatures(t *testing.T, token, repo, ns, name, version string) []string {
	t.Helper()
	status, body, raw := galaxyGet(t, "Token "+token, fmt.Sprintf("/api/galaxy/v3/plugin/ansible/content/%s/collections/index/%s/%s/versions/%s/", repo, ns, name, version))
	if status != 200 {
		t.Fatalf("version detail: %d %s", status, raw)
	}
	var out []string
	for _, s := range body["signatures"].([]any) {
		out = append(out, fmt.Sprint(s.(map[string]any)["pubkey_fingerprint"]))
	}
	return out
}

// syncRemote syncs a remote and waits for its job.
func syncRemote(t *testing.T, a *client, remote int64) (resp, string) {
	t.Helper()
	r := must(t, a.post(fmt.Sprintf("collection_remotes/%d/sync/", remote), nil), 202)
	if r.Body["type"] != "collection_sync" || r.Body["collection_sync"] != r.Body["id"] {
		t.Fatalf("sync: %s", r.Raw)
	}
	job := waitJob(t, a, fmt.Sprintf("collection_syncs/%d/", r.id()))
	return job, a.get(fmt.Sprintf("collection_syncs/%d/stdout/?format=txt", r.id())).Raw
}

func allowLocalRemotes(t *testing.T, a *client) {
	t.Helper()
	setSystemSetting(t, a, resources.HubRemoteLocalAddressesSetting, []any{"127.0.0.1", "::1"}, []any{})
}

// galaxyCredential is a Galaxy credential of a server: its URL, and the
// SSO URL its offline token goes to, when it has one.
func galaxyCredential(t *testing.T, a *client, org int64, url, authURL, token string) int64 {
	t.Helper()
	inputs := map[string]any{"url": url}
	if authURL != "" {
		inputs["auth_url"] = authURL
	}
	if token != "" {
		inputs["token"] = token
	}
	return must(t, a.post("credentials/", map[string]any{"name": uniq("upstream"), "organization": org,
		"credential_type": credType(t, a, "galaxy_api_token"), "inputs": inputs}), 201).id()
}

// TestHubRemoteSync syncs a remote from a fake galaxy_ng behind Red Hat's
// SSO: every version in a requirement's range, the highest in range of each
// dependency, only signatures the remote's keys verify; then re-syncs,
// changes, conflicts, schedules, notifications, visibility and deletion.
func TestHubRemoteSync(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	member, memberID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": memberID, "object_id": org}), 201)
	stranger, _ := newUser(t, a)
	memberPAT, strangerPAT := patOf(t, member), patOf(t, stranger)

	upstream, err := hubsign.Generate("Upstream Signer", "upstream@example.com")
	if err != nil {
		t.Fatal(err)
	}
	other, err := hubsign.Generate("Other Signer", "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGalaxy(t)
	g.offline, g.maxUses = "offline-token", 7
	ns, ns2 := galaxyName("up"), galaxyName("dep")
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "2.0.0", "2.1.0-beta.1"} {
		g.add(t, ns, "base", v, nil, upstream)
	}
	g.add(t, ns, "app", "1.0.0", map[string]string{ns + ".base": ">=1.0.0,<2.0.0", ns2 + ".util": "*"}, upstream, other)
	g.add(t, ns, "app", "1.5.0", map[string]string{ns + ".base": ">=1.1.0,<2.0.0"}, upstream)
	g.add(t, ns, "app", "2.0.0", map[string]string{ns + ".base": ">=2.0.0"}, upstream)
	g.add(t, ns2, "util", "0.1.0", nil, upstream)
	util := g.add(t, ns2, "util", "0.2.0", nil, upstream)
	g.add(t, ns, "unsigned", "1.0.0", nil)

	cred := galaxyCredential(t, a, org, g.URL+"/", g.URL+"/auth/token", "offline-token")
	keys := must(t, a.post("credentials/", map[string]any{"name": uniq("upstream keys"), "organization": org,
		"credential_type": credType(t, a, "gpg_public_key"), "inputs": map[string]any{"gpg_public_key": upstream.PublicKey}}), 201).id()
	requirements := fmt.Sprintf("collections:\n  - name: %s.app\n    version: '>=1.0.0,<2.0.0'\n  - name: %s.base\n    version: ==2.1.0-beta.1\n  - %s.unsigned\n", ns, ns, ns)

	// What a remote takes.
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": uniq("Upstream mirror"), "organization": org, "credential": cred, "requirements_file": requirements}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for _, c := range []struct {
		extra map[string]any
		want  string
	}{
		{map[string]any{"requirements_file": "roles:\n  - name: geerlingguy.java\n"}, "not roles"},
		{map[string]any{"requirements_file": "collections:\n  - name: " + ns + ".app\n    source: https://galaxy.ansible.com/\n"}, "remove source:"},
		{map[string]any{"requirements_file": "collections:\n  - name: " + ns + ".app\n    version: 1.0\n"}, "quote its version range"},
		{map[string]any{"requirements_file": "collections:\n  - name: " + ns + ".app\n    version: '~1'\n"}, "not a version range"},
		{map[string]any{"requirements_file": "collections:\n  - nonamespace\n"}, "not a collection name"},
		{map[string]any{"requirements_file": "collections: []\n"}, "lists no collections"},
		{map[string]any{"repository": "published"}, "has a repository published"},
		{map[string]any{"repository": "Not A Name"}, "lowercase letters"},
		{map[string]any{"credential": keys}, "credential"},
		{map[string]any{"signature_validation_credential": cred}, "signature_validation_credential"},
	} {
		if r := a.post("collection_remotes/", body(c.extra)); r.Status != 400 || !strings.Contains(r.Raw, c.want) {
			t.Errorf("%v: %d %s", c.extra, r.Status, r.Raw)
		}
	}
	var hubCred int64
	for _, c := range must(t, a.get("credentials/?managed=true&name="+url.QueryEscape(resources.HubCredentialName)), 200).results() {
		hubCred = int64(c["id"].(float64))
	}
	if hubCred == 0 {
		t.Fatal("no managed hub credential")
	}
	if r := a.post("collection_remotes/", body(map[string]any{"credential": hubCred})); r.Status != 400 || !strings.Contains(r.Raw, "another server") {
		t.Errorf("awxnext's own hub as a remote: %d %s", r.Status, r.Raw)
	}
	must(t, member.post("collection_remotes/", body(nil)), 403)

	created := must(t, a.post("collection_remotes/", body(map[string]any{"signature_validation_credential": keys})), 201)
	rid, repo := created.id(), fmt.Sprint(created.Body["repository"])
	if created.Body["status"] != "never updated" || !strings.HasPrefix(repo, "upstream-mirror-") || created.Body["sync_dependencies"] != true {
		t.Fatalf("created: %s", created.Raw)
	}
	galaxyCred, _ := created.Body["galaxy_credential"].(float64)
	gc := must(t, a.get(fmt.Sprintf("credentials/%d/", int64(galaxyCred))), 200)
	if gc.Body["managed"] != true || gc.Body["inputs"].(map[string]any)["url"] != "/api/galaxy/content/"+repo+"/" || gc.Body["organization"] != float64(org) {
		t.Fatalf("its credential: %s", gc.Raw)
	}
	must(t, a.patch(fmt.Sprintf("collection_remotes/%d/", rid), map[string]any{"repository": "another"}), 400)
	if r := member.get("collection_remotes/"); r.count() != 1 || member.get(fmt.Sprintf("collection_remotes/%d/", rid)).Status != 200 {
		t.Fatalf("the organization's member lists: %s", r.Raw)
	}
	if r := stranger.get("collection_remotes/"); r.count() != 0 {
		t.Fatalf("a stranger lists: %s", r.Raw)
	}
	must(t, member.post(fmt.Sprintf("collection_remotes/%d/sync/", rid), nil), 403)
	if r := must(t, member.get(fmt.Sprintf("collection_remotes/%d/sync/", rid)), 200); r.Body["can_sync"] != false {
		t.Fatalf("can_sync: %s", r.Raw)
	}

	// The fake server is on loopback, which syncs refuse unless allowed.
	job, out := syncRemote(t, a, rid)
	if job.Body["status"] != "failed" || !strings.Contains(fmt.Sprint(job.Body["job_explanation"]), resources.HubRemoteLocalAddressesSetting) {
		t.Fatalf("a sync to loopback: %s\n%s", job.Raw, out)
	}
	allowLocalRemotes(t, a)

	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	must(t, a.post(fmt.Sprintf("collection_remotes/%d/notification_templates_success/", rid), map[string]any{"id": nt}), 204)
	before, err := resources.HubPublishedState(t.Context(), app, org)
	if err != nil {
		t.Fatal(err)
	}

	job, out = syncRemote(t, a, rid)
	if job.Body["status"] != "successful" || job.Body["launch_type"] != "manual" || job.Body["collection_remote"] != float64(rid) {
		t.Fatalf("sync: %s\n%s", job.Raw, out)
	}
	t.Logf("sync output:\n%s", out)
	for _, want := range []string{
		ns + ".app >=1.0.0,<2.0.0: 1.0.0, 1.5.0",
		ns + ".base >=1.0.0,<2.0.0 (a dependency of " + ns + ".app 1.0.0): 1.2.0",
		ns + ".unsigned *: 1.0.0",
		"skipped, no signature verifies with the keys of",
		"signature 2 dropped",
		"Synced: 5 versions added, 0 already in the repository, 1 skipped, 5 signatures added.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, c := range []struct {
		ns, name string
		want     []string
	}{
		{ns, "app", []string{"1.0.0", "1.5.0"}},
		{ns, "base", []string{"1.2.0", "2.1.0-beta.1"}},
		{ns2, "util", []string{"0.2.0"}},
		{ns, "unsigned", nil},
	} {
		if got := repoVersions(t, memberPAT, repo, c.ns, c.name); !slices.Equal(got, c.want) {
			t.Errorf("%s.%s in %s: %v, want %v", c.ns, c.name, repo, got, c.want)
		}
		if got := repoVersions(t, strangerPAT, repo, c.ns, c.name); got != nil {
			t.Errorf("a stranger reads %s.%s: %v", c.ns, c.name, got)
		}
	}
	if got := repoSignatures(t, memberPAT, repo, ns, "app", "1.0.0"); !slices.Equal(got, []string{upstream.Fingerprint}) {
		t.Errorf("app 1.0.0 signed by %v, want the upstream key %s only", got, upstream.Fingerprint)
	}
	sigs := must(t, a.get(fmt.Sprintf("collection_versions/%d/signatures/", versionID(t, ns, "app", "1.0.0"))), 200).results()
	if len(sigs) != 1 || sigs[0]["origin"] != "remote" {
		t.Errorf("signatures: %v", sigs)
	}
	// Mirrored versions stay in the remote's repository.
	if got := repoVersions(t, memberPAT, "published", ns, "app"); got != nil {
		t.Errorf("published has %v", got)
	}
	if g.refreshes < 2 {
		t.Errorf("%d access tokens: the sync didn't refresh an expired one", g.refreshes)
	}
	after, _ := resources.HubPublishedState(t.Context(), app, org)
	if after == before {
		t.Error("the organization's published state didn't change: jobs would install from a stale cache")
	}
	if reqs := rec.wait(t, 1); reqs[0].Body["friendly_name"] != "Collection Sync" || reqs[0].Body["status"] != "successful" {
		t.Errorf("notification: %v", reqs[0].Body)
	}
	if r := must(t, a.get(fmt.Sprintf("collection_remotes/%d/", rid)), 200); r.Body["status"] != "successful" || r.Body["last_job_run"] == nil {
		t.Errorf("the remote after its sync: %s", r.Raw)
	}
	if r := must(t, a.get(fmt.Sprintf("collection_remotes/%d/collection_syncs/", rid)), 200); r.count() != 2 {
		t.Errorf("its syncs: %s", r.Raw)
	}
	if r := must(t, a.get(fmt.Sprintf("unified_jobs/?type=collection_sync&unified_job_template=%d", rid)), 200); r.count() != 2 {
		t.Errorf("unified jobs: %s", r.Raw)
	}
	if r := must(t, member.get(fmt.Sprintf("collection_remotes/%d/collection_syncs/", rid)), 200); r.count() != 2 {
		t.Errorf("the member's view of its syncs: %s", r.Raw)
	}
	if r := stranger.get(fmt.Sprintf("collection_syncs/%d/", job.id())); r.Status != 404 && r.Status != 403 {
		t.Errorf("a stranger reads the sync: %d", r.Status)
	}

	// Again: nothing new. Then upstream releases a version in a
	// dependency's range: it comes, the one before stays.
	job, out = syncRemote(t, a, rid)
	if job.Body["status"] != "successful" || !strings.Contains(out, "0 versions added, 5 already in the repository, 1 skipped, 0 signatures added") {
		t.Fatalf("re-sync: %s\n%s", job.Raw, out)
	}
	g.add(t, ns, "base", "1.3.0", nil, upstream)
	job, out = syncRemote(t, a, rid)
	if job.Body["status"] != "successful" || !slices.Equal(repoVersions(t, memberPAT, repo, ns, "base"), []string{"1.2.0", "1.3.0", "2.1.0-beta.1"}) {
		t.Fatalf("sync after a release: %s\n%s", job.Raw, out)
	}

	// Upstream rebuilds a version the hub has, and a collection is gone:
	// the hub keeps its version, the sync fails and says why.
	g.replace(t, util)
	must(t, a.patch(fmt.Sprintf("collection_remotes/%d/", rid), map[string]any{"requirements_file": requirements + "  - " + ns + ".gone\n"}), 200)
	job, out = syncRemote(t, a, rid)
	explanation := fmt.Sprint(job.Body["job_explanation"])
	if job.Body["status"] != "failed" || !strings.Contains(explanation, "2 problems") || !strings.Contains(explanation, "the hub has another artifact of it") ||
		!strings.Contains(explanation, "The server has no collection "+ns+".gone") {
		t.Fatalf("sync with a rebuilt version: %s\n%s", job.Raw, out)
	}
	if !hasVersion(t, ns2, "util", "0.2.0") {
		t.Fatal("the hub lost util 0.2.0")
	}

	// Scheduled like a project update.
	sched := must(t, a.post(fmt.Sprintf("collection_remotes/%d/schedules/", rid), map[string]any{"name": uniq("nightly"),
		"rrule": "DTSTART:20300101T020000Z RRULE:FREQ=DAILY;INTERVAL=1"}), 201)
	if sf := sched.Body["summary_fields"].(map[string]any)["unified_job_template"].(map[string]any); sf["unified_job_type"] != "collection_sync" {
		t.Errorf("schedule: %s", sched.Raw)
	}
	if r := must(t, a.get(fmt.Sprintf("collection_remotes/%d/", rid)), 200); r.Body["next_job_run"] == nil {
		t.Errorf("next_job_run: %s", r.Raw)
	}

	// A remote without keys keeps the signatures as the server gives them.
	ns3 := galaxyName("other")
	g.add(t, ns3, "tools", "1.0.0", nil, other)
	loose := must(t, a.post("collection_remotes/", map[string]any{"name": uniq("loose"), "organization": org, "credential": cred,
		"requirements_file": "collections:\n  - " + ns3 + ".tools\n", "repository": galaxyName("loose")}), 201)
	job, out = syncRemote(t, a, loose.id())
	if job.Body["status"] != "successful" {
		t.Fatalf("sync without keys: %s\n%s", job.Raw, out)
	}
	if got := repoSignatures(t, memberPAT, fmt.Sprint(loose.Body["repository"]), ns3, "tools", "1.0.0"); !slices.Equal(got, []string{other.Fingerprint}) {
		t.Errorf("tools signed by %v, want %s", got, other.Fingerprint)
	}

	// Deleting a remote deletes its repository, the versions only it held,
	// and its credential.
	must(t, a.delete(fmt.Sprintf("collection_remotes/%d/", loose.id())), 204)
	if hasVersion(t, ns3, "tools", "1.0.0") {
		t.Error("tools 1.0.0 outlived its remote")
	}
	if n := countRows(t, "SELECT count(*) FROM collection_repositories WHERE name = $1", loose.Body["repository"]); n != 0 {
		t.Errorf("%d repositories left", n)
	}
	must(t, a.get(fmt.Sprintf("credentials/%d/", int64(loose.Body["galaxy_credential"].(float64)))), 404)
}

func hasVersion(t *testing.T, ns, name, version string) bool {
	t.Helper()
	return countRows(t, `SELECT count(*) FROM collection_versions v JOIN collections c ON c.id = v.collection_id
		JOIN collection_namespaces t ON t.id = c.namespace_id WHERE t.name = $1 AND c.name = $2 AND v.version = $3`, ns, name, version) > 0
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := app.DB.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestHubRemoteAnsibleGalaxy: ansible-galaxy installs mirrored collections
// from the remote's repository, checking their mirrored signatures; then
// what ansible-galaxy collection download wrote from the server, with
// signatures beside it, goes into a hub through awxnext hub import.
func TestHubRemoteAnsibleGalaxy(t *testing.T) {
	requireAnsible(t)
	for _, tool := range []string{"ansible-galaxy", "gpg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	a := adminClient(t)
	org := newOrg(t, a)
	member, memberID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": memberID, "object_id": org}), 201)
	pat := patOf(t, member)
	upstream, err := hubsign.Generate("Upstream Signer", "upstream@example.com")
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGalaxy(t)
	g.token = "upstream-token"
	ns := galaxyName("up")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		g.add(t, ns, "base", v, nil, upstream)
	}
	app1 := g.add(t, ns, "app", "1.0.0", map[string]string{ns + ".base": ">=1.0.0"}, upstream)
	allowLocalRemotes(t, a)

	remote := must(t, a.post("collection_remotes/", map[string]any{"name": uniq("mirror"), "organization": org,
		"credential": galaxyCredential(t, a, org, g.URL+"/api/", "", "upstream-token"), "requirements_file": "collections:\n  - name: " + ns + ".app\n"}), 201)
	repo := fmt.Sprint(remote.Body["repository"])
	if job, out := syncRemote(t, a, remote.id()); job.Body["status"] != "successful" {
		t.Fatalf("sync: %s\n%s", job.Raw, out)
	}

	keyring := func(dir string) string {
		t.Helper()
		kr, err := hubsign.Keyring(upstream.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "keyring.gpg")
		if err := os.WriteFile(p, kr, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hub := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/"+repo+"/", pat)
	dest := filepath.Join(hub.dir, "installed")
	hub.mustRun("collection", "install", ns+".app", "-p", dest, "--keyring", keyring(hub.dir), "--required-valid-signature-count", "+1", "--no-cache")
	if a, b := installedVersion(t, dest, ns, "app"), installedVersion(t, dest, ns, "base"); a != "1.0.0" || b != "1.1.0" {
		t.Fatalf("installed app %q and base %q from the remote's repository", a, b)
	}

	// Downloaded on a machine with internet access, from the server.
	up := newAnsibleGalaxy(t, g.URL+"/api/", "upstream-token")
	dir := filepath.Join(up.dir, "download")
	up.mustRun("collection", "download", ns+".app", "-p", dir, "--no-cache")
	for _, f := range []string{app1.file(), ns + "-base-1.1.0.tar.gz", "requirements.yml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("download: %v", err)
		}
	}
	// Signatures beside the artifacts: an .asc file, and one that
	// requirements.yml names.
	os.WriteFile(filepath.Join(dir, app1.file()+".asc"), []byte(app1.sigs[0]), 0o644)
	base := g.versions[ns+".base"][1]
	os.WriteFile(filepath.Join(dir, "base.sig"), []byte(base.sigs[0]), 0o644)
	req, _ := os.ReadFile(filepath.Join(dir, "requirements.yml"))
	req = []byte(strings.Replace(string(req), "version: 1.1.0", "version: 1.1.0\n  signatures:\n  - file://base.sig", 1))
	os.WriteFile(filepath.Join(dir, "requirements.yml"), req, 0o644)

	// Into another hub: here, the published repository after deleting the
	// mirrored versions. Their namespaces stay.
	must(t, a.delete(fmt.Sprintf("collection_remotes/%d/", remote.id())), 204)
	if hasVersion(t, ns, "app", "1.0.0") {
		t.Fatal("app 1.0.0 outlived its remote")
	}
	bin := buildAwxnext(t)
	keyFile := filepath.Join(t.TempDir(), "upstream.asc")
	os.WriteFile(keyFile, []byte(upstream.PublicKey), 0o644)
	env := append(os.Environ(), "AWXNEXT_DATABASE_URL="+app.Cfg.DatabaseURL, "AWXNEXT_SECRET_KEY="+app.Cfg.SecretKey,
		"AWXNEXT_PROJECTS_ROOT="+app.Cfg.ProjectsRoot, "AWXNEXT_JOBS_ROOT="+app.Cfg.JobsRoot)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"hub", "import"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	orgName := fmt.Sprint(must(t, a.get(fmt.Sprintf("organizations/%d/", org)), 200).Body["name"])
	out, err := run("--keyring", keyFile, dir)
	if err != nil || !strings.Contains(out, "Imported into published: 2 added, 0 already there, 0 skipped, 0 failed.") {
		t.Fatalf("hub import: %v\n%s", err, out)
	}
	if out, err := run(dir); err != nil || !strings.Contains(out, "0 added, 2 already there") {
		t.Fatalf("hub import again: %v\n%s", err, out)
	}
	for _, v := range []struct{ name, version string }{{"app", "1.0.0"}, {"base", "1.1.0"}} {
		sigs := must(t, a.get(fmt.Sprintf("collection_versions/%d/signatures/", versionID(t, ns, v.name, v.version))), 200).results()
		var origins []string
		for _, s := range sigs {
			origins = append(origins, fmt.Sprint(s["origin"], "/", s["pubkey_fingerprint"]))
		}
		sort.Strings(origins)
		// The hub signs what enters published, besides the imported signature.
		if len(origins) != 2 || !slices.Contains(origins, "import/"+upstream.Fingerprint) || !strings.HasPrefix(origins[0], "awxnext/") {
			t.Errorf("%s signatures: %v", v.name, origins)
		}
	}
	pub := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/published/", pat)
	pdest := filepath.Join(pub.dir, "installed")
	pub.mustRun("collection", "install", ns+".app", "-p", pdest, "--keyring", keyring(pub.dir), "--required-valid-signature-count", "+1", "--no-cache")
	if v := installedVersion(t, pdest, ns, "base"); v != "1.1.0" {
		t.Fatalf("installed base %q from published", v)
	}

	// A keyring the signatures don't verify with: nothing is imported.
	stray, _ := hubsign.Generate("Stray", "stray@example.com")
	strayFile := filepath.Join(t.TempDir(), "stray.asc")
	os.WriteFile(strayFile, []byte(stray.PublicKey), 0o644)
	ns2 := galaxyName("strict")
	g.add(t, ns2, "only", "1.0.0", nil, upstream)
	dir2 := filepath.Join(up.dir, "download2")
	up.mustRun("collection", "download", ns2+".only", "-p", dir2, "--no-cache")
	only := g.versions[ns2+".only"][0]
	os.WriteFile(filepath.Join(dir2, only.file()+".asc"), []byte(only.sigs[0]), 0o644)
	if out, err := run(dir2); err == nil || !strings.Contains(out, "The hub has no namespace "+ns2) || !strings.Contains(out, "1 of 1 collections failed to import") {
		t.Fatalf("import of a new namespace without an organization: %v\n%s", err, out)
	}
	if out, err := run("--organization", orgName, "--keyring", strayFile, dir2); err != nil || !strings.Contains(out, "skipped, no signature verifies with the keys of the keyring") {
		t.Fatalf("import with another keyring: %v\n%s", err, out)
	}
	if hasVersion(t, ns2, "only", "1.0.0") {
		t.Error("an unverified collection was imported")
	}
}

// TestHubRemoteGalaxyAnsibleCom mirrors a small collection and its
// dependency from galaxy.ansible.com. It needs internet access: full.yml
// runs it with AWXNEXT_TEST_GALAXY_REMOTE=1.
func TestHubRemoteGalaxyAnsibleCom(t *testing.T) {
	if os.Getenv("AWXNEXT_TEST_GALAXY_REMOTE") == "" {
		t.Skip("set AWXNEXT_TEST_GALAXY_REMOTE=1 to mirror from galaxy.ansible.com")
	}
	a := adminClient(t)
	org := newOrg(t, a)
	// A proxy on this host, as in a sandbox, is a local address.
	allowLocalRemotes(t, a)
	remote := must(t, a.post("collection_remotes/", map[string]any{"name": uniq("galaxy"), "organization": org,
		"credential":        galaxyCredential(t, a, org, "https://galaxy.ansible.com/", "", ""),
		"requirements_file": "collections:\n  - name: community.docker\n    version: '>=4.8.0,<4.8.2'\n"}), 201)
	repo := fmt.Sprint(remote.Body["repository"])
	job, out := syncRemote(t, a, remote.id())
	if job.Body["status"] != "successful" {
		t.Fatalf("sync from galaxy.ansible.com: %s\n%s", job.Raw, out)
	}
	t.Logf("sync output:\n%s", out)
	admin := patOf(t, a)
	if got := repoVersions(t, admin, repo, "community", "docker"); !slices.Equal(got, []string{"4.8.0", "4.8.1"}) {
		t.Errorf("community.docker: %v", got)
	}
	if got := repoVersions(t, admin, repo, "community", "library_inventory_filtering_v1"); len(got) != 1 {
		t.Errorf("its dependency: %v", got)
	}
}

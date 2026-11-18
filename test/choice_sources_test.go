package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeCMDB is a JSON API with VLANs by site (a token in Authorization), an
// OAuth token endpoint and groups behind its token, and a list behind
// basic authentication.
func fakeCMDB(t *testing.T, vlanCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	vlans := []map[string]any{
		{"vid": 100, "name": "prod", "site": "dc1"},
		{"vid": 200, "name": "dev", "site": "dc1"},
		{"vid": 300, "name": "dmz", "site": "dc2"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/vlans/":
			vlanCalls.Add(1)
			if r.Header.Get("Authorization") != "Token cmdb-token" {
				w.WriteHeader(401)
				return
			}
			if r.URL.Query().Get("pin") != "" {
				w.WriteHeader(418)
				return
			}
			out := []map[string]any{}
			for _, v := range vlans {
				if site := r.URL.Query().Get("site"); site == "" || v["site"] == site {
					out = append(out, v)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"count": len(out), "results": out})
		case "/oauth/token":
			r.ParseForm()
			id, secret, _ := r.BasicAuth()
			if id == "" {
				id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
			}
			if id != "graph-client" || secret != "graph-secret" || r.PostForm.Get("grant_type") != "client_credentials" {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "graph-token", "token_type": "Bearer", "expires_in": 3600})
		case "/v1.0/groups":
			if r.Header.Get("Authorization") != "Bearer graph-token" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"displayName": "ops"}, map[string]any{"displayName": "dba"}}})
		case "/basic/sites":
			if u, p, ok := r.BasicAuth(); !ok || u != "reader" || p != "reader-pw" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode([]any{"dc1", "dc2"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setJobsSettings(t *testing.T, a *client, values map[string]any) {
	t.Helper()
	must(t, a.patch("settings/jobs/", values), 200)
	t.Cleanup(func() {
		reset := map[string]any{}
		for k := range values {
			reset[k] = []any{}
		}
		a.patch("settings/jobs/", reset)
	})
}

func choicesOf(t *testing.T, c *client, path, variable string, answers map[string]any) (list []string, labels map[string]any, errText string) {
	t.Helper()
	q := url.Values{"variable": {variable}}
	if answers != nil {
		b, _ := json.Marshal(answers)
		q.Set("answers", string(b))
	}
	r := must(t, c.get(path+"survey_choices/?"+q.Encode()), 200)
	for _, v := range r.Body["choices"].([]any) {
		list = append(list, fmt.Sprint(v))
	}
	labels, _ = r.Body["labels"].(map[string]any)
	errText, _ = r.Body["error"].(string)
	return
}

// An http source lists choices from a JSON API that the server calls with a
// credential, narrowed by an earlier answer; answers outside its list are
// refused; lists are cached; and only the hosts and addresses an
// administrator allows are called.
func TestChoiceSourcesHTTP(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	var vlanCalls atomic.Int32
	cmdb := fakeCMDB(t, &vlanCalls)
	host := strings.TrimPrefix(cmdb.URL, "http://")
	token := must(t, a.post("credentials/", map[string]any{"name": uniq("cmdb"), "organization": f.org, "credential_type": credType(t, a, "http_api_token"),
		"inputs": map[string]any{"token": "cmdb-token", "prefix": "Token"}}), 201).id()
	source := map[string]any{"name": uniq("vlans"), "organization": f.org, "kind": "http", "url": cmdb.URL + "/api/vlans/",
		"parameters": map[string]any{"site": "{{ answers.datacenter }}", "pin": "{{ answers.pin }}"}, "credential": token,
		"items": "data.results", "value": "string(item.vid)", "label": `item.name + " (" + string(item.vid) + ")"`,
		"filter": `(answers.env != "prod" || item.name == "prod") && answers.late != "hide"`}

	// Only hosts an administrator lists.
	r := must(t, a.post("choice_sources/", source), 400)
	if !strings.Contains(r.Raw, "CHOICE_SOURCE_HOSTS") {
		t.Fatalf("unlisted host: %s", r.Raw)
	}
	setJobsSettings(t, a, map[string]any{"CHOICE_SOURCE_HOSTS": []any{"http://" + host, "http://169.254.169.254"}})

	// Only the credential types meant for APIs.
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "u"}}), 201).id()
	bad := map[string]any{}
	for k, v := range source {
		bad[k] = v
	}
	bad["credential"], bad["name"] = ssh, uniq("bad")
	must(t, a.post("choice_sources/", bad), 400)
	bad["credential"], bad["value"] = token, "item.vid ==="
	must(t, a.post("choice_sources/", bad), 400)

	src := must(t, a.post("choice_sources/", source), 201).id()

	// Loopback is awxnext's own host: refused until allowed.
	test := must(t, a.post(fmt.Sprintf("choice_sources/%d/test/", src), map[string]any{"answers": map[string]any{"datacenter": "dc1"}}), 200).Body
	if !strings.Contains(fmt.Sprint(test["error"]), "CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES") || test["count"] != float64(0) {
		t.Fatalf("loopback was called: %v", test)
	}
	setJobsSettings(t, a, map[string]any{"CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES": []any{"127.0.0.1"}})

	// The Test button: the API's list for the answers given, and a sample.
	test = must(t, a.post(fmt.Sprintf("choice_sources/%d/test/", src), map[string]any{"answers": map[string]any{"datacenter": "dc1"}}), 200).Body
	if test["error"] != nil || test["count"] != float64(2) || fmt.Sprint(test["choices"]) != "[map[label:prod (100) value:100] map[label:dev (200) value:200]]" ||
		len(test["sample"].([]any)) != 2 || !strings.Contains(fmt.Sprint(test["url"]), "site=dc1") {
		t.Fatalf("test: %v", test)
	}

	// A survey lists it: one question narrows the next.
	jt := f.template(t, "site.yml", map[string]any{"survey_enabled": true})
	spec := map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Datacenter", "variable": "datacenter", "type": "multiplechoice", "choices": []any{"dc1", "dc2", "dc3"}, "required": true},
		map[string]any{"question_name": "Environment", "variable": "env", "type": "multiplechoice", "choices": []any{"prod", "dev"}, "required": true},
		map[string]any{"question_name": "PIN", "variable": "pin", "type": "password", "required": false},
		map[string]any{"question_name": "VLAN", "variable": "vlan", "type": "multiplechoice", "required": true, "choices_from": map[string]any{"type": "source", "source": src}},
		map[string]any{"question_name": "Late", "variable": "late", "type": "text", "required": false},
	}}
	// Survey owners need Use on the source.
	owner, ownerID := newUser(t, a)
	grant(t, a, "JobTemplate Admin", ownerID, jt)
	must(t, owner.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), spec), 403)
	grant(t, a, "ChoiceSource Use", ownerID, src)
	must(t, owner.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), spec), 200)
	if r := owner.get(fmt.Sprintf("choice_sources/%d/", src)); r.Status != 200 || r.Body["credential"] != float64(token) {
		t.Fatalf("a user of the source sees it: %d %s", r.Status, r.Raw)
	}
	if r := owner.get(fmt.Sprintf("credentials/%d/", token)); r.Status != 403 && r.Status != 404 {
		t.Errorf("a user of the source reads its credential: %d", r.Status)
	}

	tpl := fmt.Sprintf("job_templates/%d/", jt)
	calls := vlanCalls.Load()
	// Password answers never reach the API (it refuses a pin).
	list, labels, errText := choicesOf(t, a, tpl, "vlan", map[string]any{"datacenter": "dc2", "env": "dev", "pin": "pw-123"})
	if fmt.Sprint(list) != "[300]" || labels["300"] != "dmz (300)" || errText != "" {
		t.Fatalf("dc2: %v %v %s", list, labels, errText)
	}
	// The filter uses the earlier answers, and only those.
	if list, _, _ := choicesOf(t, a, tpl, "vlan", map[string]any{"datacenter": "dc1", "env": "prod"}); fmt.Sprint(list) != "[100]" {
		t.Fatalf("dc1, prod: %v", list)
	}
	if list, _, _ := choicesOf(t, a, tpl, "vlan", map[string]any{"datacenter": "dc1", "env": "dev", "late": "hide"}); fmt.Sprint(list) != "[100 200]" {
		t.Fatalf("a later answer narrowed the list: %v", list)
	}
	if got := vlanCalls.Load() - calls; got != 2 {
		t.Errorf("API calls for dc2 and dc1: %d, want 2 (one per site)", got)
	}

	// At launch the answer is checked against the list, from the cache.
	launch := func(answers map[string]any) resp {
		return a.post(tpl+"launch/", map[string]any{"extra_vars": answers})
	}
	if r := launch(map[string]any{"datacenter": "dc1", "env": "dev", "vlan": "300"}); r.Status != 400 || !strings.Contains(r.Raw, "expected to be one of [100 200]") {
		t.Fatalf("an answer outside the list: %d %s", r.Status, r.Raw)
	}
	job := must(t, launch(map[string]any{"datacenter": "dc1", "env": "dev", "pin": "pw-123", "vlan": "200"}), 201).id()
	if ev := fmt.Sprint(must(t, a.get(fmt.Sprintf("jobs/%d/", job)), 200).Body["extra_vars"]); !strings.Contains(ev, `"vlan": "200"`) {
		t.Errorf("extra_vars: %s", ev)
	}
	if got := vlanCalls.Load() - calls; got != 2 {
		t.Errorf("the launch called the API again: %d calls", got)
	}

	// A catalog request keeps the label it was shown.
	item := must(t, a.post("catalog_items/", map[string]any{"name": uniq("net"), "unified_job_template": jt}), 201).id()
	if list, labels, _ := choicesOf(t, a, fmt.Sprintf("catalog_items/%d/", item), "vlan", map[string]any{"datacenter": "dc1", "env": "prod"}); fmt.Sprint(list) != "[100]" || labels["100"] != "prod (100)" {
		t.Fatalf("item choices: %v %v", list, labels)
	}
	req := must(t, a.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{"answers": map[string]any{"datacenter": "dc1", "env": "prod", "vlan": "100"}}), 201).Body
	if fmt.Sprint(req["answer_labels"]) != "map[vlan:prod (100)]" {
		t.Fatalf("request labels: %v", req["answer_labels"])
	}

	// When the API can't be reached: the cached list still answers; with
	// nothing cached, the launch is refused with the reason.
	cmdb.Close()
	must(t, launch(map[string]any{"datacenter": "dc2", "env": "dev", "vlan": "300"}), 201)
	if r := launch(map[string]any{"datacenter": "dc3", "env": "dev", "vlan": "300"}); r.Status != 400 || !strings.Contains(r.Raw, "Could not list the choices for 'vlan'") ||
		!strings.Contains(r.Raw, "calling http://"+host) {
		t.Fatalf("API down, nothing cached: %d %s", r.Status, r.Raw)
	}

	// A source aimed at a refused address lists nothing.
	meta := must(t, a.post("choice_sources/", map[string]any{"name": uniq("meta"), "organization": f.org, "url": "http://169.254.169.254/latest/meta-data/"}), 201).id()
	jt2 := f.template(t, "site.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt2), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Role", "variable": "role", "type": "multiplechoice", "required": true, "choices_from": map[string]any{"type": "source", "source": meta}},
	}}), 200)
	if list, _, errText := choicesOf(t, a, fmt.Sprintf("job_templates/%d/", jt2), "role", nil); len(list) != 0 || !strings.Contains(errText, "169.254.169.254") {
		t.Fatalf("refused address: %v %q", list, errText)
	}

	// A source that surveys list can't be deleted; an unused one can.
	if r := a.delete(fmt.Sprintf("choice_sources/%d/", src)); r.Status != 409 || !strings.Contains(r.Raw, "take it out") {
		t.Fatalf("delete a source in use: %d %s", r.Status, r.Raw)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt2), map[string]any{}), 200)
	must(t, a.delete(fmt.Sprintf("choice_sources/%d/", meta)), 204)
}

// OAuth client credentials and basic authentication; who may create and
// use sources.
func TestChoiceSourceCredentials(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	var calls atomic.Int32
	cmdb := fakeCMDB(t, &calls)
	setJobsSettings(t, a, map[string]any{"CHOICE_SOURCE_HOSTS": []any{cmdb.URL}, "CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES": []any{"127.0.0.1"}})

	oauth := must(t, a.post("credentials/", map[string]any{"name": uniq("graph"), "organization": org, "credential_type": credType(t, a, "oauth2_client_credentials"),
		"inputs": map[string]any{"token_url": cmdb.URL + "/oauth/token", "client_id": "graph-client", "client_secret": "graph-secret", "scope": "https://graph.microsoft.com/.default"}}), 201).id()
	basic := must(t, a.post("credentials/", map[string]any{"name": uniq("basic"), "organization": org, "credential_type": credType(t, a, "http_api_basic"),
		"inputs": map[string]any{"username": "reader", "password": "reader-pw"}}), 201).id()

	groups := must(t, a.post("choice_sources/", map[string]any{"name": uniq("groups"), "organization": org, "url": cmdb.URL + "/v1.0/groups",
		"credential": oauth, "items": "data.value", "value": "item.displayName"}), 201).id()
	if test := must(t, a.post(fmt.Sprintf("choice_sources/%d/test/", groups), map[string]any{}), 200).Body; fmt.Sprint(test["choices"]) != "[map[label:ops value:ops] map[label:dba value:dba]]" {
		t.Fatalf("oauth: %v", test)
	}
	sites := must(t, a.post("choice_sources/", map[string]any{"name": uniq("sites"), "organization": org, "url": cmdb.URL + "/basic/sites", "credential": basic}), 201).id()
	if test := must(t, a.post(fmt.Sprintf("choice_sources/%d/test/", sites), nil), 200).Body; test["count"] != float64(2) {
		t.Fatalf("basic: %v", test)
	}
	// A wrong secret: the API's refusal, without the secret.
	must(t, a.patch(fmt.Sprintf("credentials/%d/", oauth), map[string]any{"inputs": map[string]any{"token_url": cmdb.URL + "/oauth/token", "client_id": "graph-client", "client_secret": "wrong"}}), 200)
	test := must(t, a.post(fmt.Sprintf("choice_sources/%d/test/", groups), nil), 200).Body
	if e := fmt.Sprint(test["error"]); !strings.Contains(e, "401") || strings.Contains(e, "wrong") {
		t.Fatalf("bad secret: %v", test)
	}

	// Organization admins create sources; members don't, and need Use on a
	// credential to give it to one.
	member, memberID := newUser(t, a)
	grant(t, a, "Organization Member", memberID, org)
	must(t, member.post("choice_sources/", map[string]any{"name": uniq("x"), "organization": org, "url": cmdb.URL + "/basic/sites"}), 403)
	admin, adminID := newUser(t, a)
	grant(t, a, "Organization Admin", adminID, org)
	other := newOrg(t, a)
	foreign := must(t, a.post("credentials/", map[string]any{"name": uniq("basic"), "organization": other, "credential_type": credType(t, a, "http_api_basic"),
		"inputs": map[string]any{"username": "reader", "password": "reader-pw"}}), 201).id()
	must(t, admin.post("choice_sources/", map[string]any{"name": uniq("x"), "organization": org, "url": cmdb.URL + "/basic/sites", "credential": foreign}), 403)
	must(t, admin.post("choice_sources/", map[string]any{"name": uniq("x"), "organization": org, "url": cmdb.URL + "/basic/sites", "credential": basic}), 201)
	if r := admin.post("choice_sources/", map[string]any{"name": uniq("x"), "organization": org, "kind": "job"}); r.Status != 400 || !strings.Contains(r.Raw, "job_template") {
		t.Fatalf("job source without a template: %d %s", r.Status, r.Raw)
	}
}

// A job source lists what its template's run published with set_stats,
// keeps it when a later run fails, and refreshes on its schedule.
func TestChoiceSourceJob(t *testing.T) {
	w := newOutputsFixture(t)
	a := w.a
	images := []any{map[string]any{"name": "rhel9", "arch": "x86_64"}, map[string]any{"name": "ubuntu24", "arch": "arm64"}}
	publisher := w.template(t, "outputs.yml", map[string]any{"extra_vars": map[string]any{"publish": map[string]any{"images": images}}})
	src := must(t, a.post("choice_sources/", map[string]any{"name": uniq("images"), "organization": w.org, "kind": "job", "job_template": publisher,
		"items": "data.images", "value": "item.name", "label": `item.name + " · " + item.arch`, "filter": `user.username != "" && (answers.arch == nil || item.arch == answers.arch)`}), 201).Body
	id := int64(src["id"].(float64))
	path := fmt.Sprintf("choice_sources/%d/", id)
	if src["next_refresh"] != nil {
		t.Errorf("a source refreshed by hand has a next refresh: %v", src["next_refresh"])
	}
	if test := must(t, a.post(path+"test/", nil), 200).Body; !strings.Contains(fmt.Sprint(test["error"]), "no list yet") {
		t.Fatalf("before a refresh: %v", test)
	}

	refresh := func() resp {
		r := must(t, a.post(path+"refresh/", nil), 202)
		job := int64(r.Body["job"].(float64))
		waitJob(t, a, fmt.Sprintf("jobs/%d/", job))
		var got resp
		waitFor(t, "the source to read the job", func() bool {
			got = a.get(path)
			return got.Body["last_job"] == float64(job) && (got.Body["last_refresh_job"] == float64(job) || got.Body["last_error"] != "")
		})
		return got
	}
	first := refresh()
	good := first.Body["last_refresh_job"]
	if good == nil || first.Body["last_error"] != "" || first.Body["last_refresh"] == nil {
		t.Fatalf("after a refresh: %v", first.Body)
	}
	if j := must(t, a.get(fmt.Sprintf("jobs/%d/", int64(good.(float64)))), 200).Body; j["launch_type"] != "choice_source" {
		t.Errorf("refresh job's launch type: %v", j["launch_type"])
	}

	consumer := w.template(t, "outputs.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", consumer), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Arch", "variable": "arch", "type": "multiplechoice", "choices": []any{"x86_64", "arm64"}, "required": false},
		map[string]any{"question_name": "Image", "variable": "image", "type": "multiplechoice", "required": true, "choices_from": map[string]any{"type": "source", "source": id}},
	}}), 200)
	tpl := fmt.Sprintf("job_templates/%d/", consumer)
	if list, labels, _ := choicesOf(t, a, tpl, "image", nil); fmt.Sprint(list) != "[rhel9 ubuntu24]" || labels["rhel9"] != "rhel9 · x86_64" {
		t.Fatalf("published list: %v %v", list, labels)
	}
	if list, _, _ := choicesOf(t, a, tpl, "image", map[string]any{"arch": "arm64"}); fmt.Sprint(list) != "[ubuntu24]" {
		t.Fatalf("filtered: %v", list)
	}

	// A run that publishes no list, or fails, keeps the last list, and says so.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", publisher), map[string]any{"extra_vars": map[string]any{"publish": map[string]any{"other": 1}}}), 200)
	empty := refresh()
	if empty.Body["last_refresh_job"] != good || !strings.Contains(fmt.Sprint(empty.Body["last_error"]), "published nothing that reads as a list") {
		t.Fatalf("after a refresh that published no list: %v", empty.Body)
	}
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", publisher), map[string]any{"extra_vars": map[string]any{"publish": "{{ lookup('file', '/nonexistent/choices') }}"}}), 200)
	second := refresh()
	if second.Body["last_refresh_job"] != good || !strings.Contains(fmt.Sprint(second.Body["last_error"]), " failed;") {
		t.Fatalf("after a failed refresh: %v", second.Body)
	}
	if list, _, _ := choicesOf(t, a, tpl, "image", nil); fmt.Sprint(list) != "[rhel9 ubuntu24]" {
		t.Fatalf("the list after a failed refresh: %v", list)
	}
	if r := a.post(tpl+"launch/", map[string]any{"extra_vars": map[string]any{"image": "fedora"}}); r.Status != 400 {
		t.Fatalf("an image outside the list: %d %s", r.Status, r.Raw)
	}

	// New expressions must read the published list; then it is read again.
	must(t, a.patch(path, map[string]any{"value": "item"}), 400)
	must(t, a.patch(path, map[string]any{"label": "upper(item.name)"}), 200)
	if _, labels, _ := choicesOf(t, a, tpl, "image", nil); labels["rhel9"] != "RHEL9" {
		t.Fatalf("labels after a change: %v", labels)
	}

	// On a schedule.
	sched := must(t, a.post("choice_sources/", map[string]any{"name": uniq("sched"), "organization": w.org, "kind": "job", "job_template": publisher,
		"items": "data.images", "refresh_minutes": 60}), 201).Body
	if sched["next_refresh"] == nil {
		t.Fatalf("no next refresh: %v", sched)
	}
	sp := fmt.Sprintf("choice_sources/%d/", int64(sched["id"].(float64)))
	waitFor(t, "the scheduled refresh", func() bool { return a.get(sp).Body["last_job"] != nil })
	if next := a.get(sp).Body["next_refresh"]; next == nil || fmt.Sprint(next) <= fmt.Sprint(sched["next_refresh"]) {
		t.Errorf("next refresh not moved: %v", next)
	}
}

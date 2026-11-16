package test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/eventstreams"
)

const edaAPI = "/api/eda/v1/"

func eda(path string) string { return edaAPI + path }

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// EDA's pagination, errors and auth, over awxnext's organizations, teams,
// users, roles and controller tokens.
func TestEDAAPI(t *testing.T) {
	a := adminClient(t)
	if root := must(t, (&client{t: t}).get("/api/"), 200).Body; fmt.Sprint(root["apis"]) != "map[eda:/api/eda/]" {
		t.Errorf("API root: %v", root)
	}
	if v := must(t, (&client{t: t}).get("/api/eda/"), 200).Body; !strings.HasSuffix(fmt.Sprint(v["current_version"]), "/api/eda/v1/") {
		t.Errorf("EDA's root: %v", v)
	}
	cfg := must(t, a.get(eda("config/")), 200).Body
	if cfg["deployment_type"] != "awxnext" || cfg["version"] == "" {
		t.Errorf("config: %v", cfg)
	}
	must(t, (&client{t: t, user: "admin", pass: "wrong"}).get(eda("config/")), 401)
	must(t, (&client{t: t, token: "nope"}).get(eda("organizations/")), 401)
	must(t, a.get(eda("nothing/")), 404)

	prefix := uniq("edaorg")
	for i := range 3 {
		must(t, a.post(eda("organizations/"), map[string]any{"name": fmt.Sprintf("%s-%d", prefix, i), "description": "d"}), 201)
	}
	page := must(t, a.get(eda("organizations/?page_size=2&name="+prefix)), 200).Body
	if page["count"] != float64(3) || page["page"] != float64(1) || page["page_size"] != float64(2) || page["previous"] != nil ||
		len(page["results"].([]any)) != 2 {
		t.Fatalf("page 1: %v", page)
	}
	next, err := url.Parse(page["next"].(string))
	if err != nil || next.Path != eda("organizations/") || next.Query().Get("page") != "2" {
		t.Fatalf("next: %v", page["next"])
	}
	page2 := must(t, a.get(next.RequestURI()), 200).Body
	if len(page2["results"].([]any)) != 1 || page2["next"] != nil || page2["previous"] == nil {
		t.Errorf("page 2: %v", page2)
	}
	if r := must(t, a.get(eda("organizations/?page=9&name="+prefix)), 200).Body; len(r["results"].([]any)) != 0 {
		t.Errorf("page 9: %v", r)
	}
	must(t, a.get(eda("organizations/?page=0")), 404)
	org := page["results"].([]any)[0].(map[string]any)
	if org["created_at"] == nil || org["managed"] != false || org["created_by"] == nil {
		t.Errorf("organization: %v", org)
	}
	orgID := num(org["id"])

	team := must(t, a.post(eda("teams/"), map[string]any{"name": uniq("team"), "organization_id": orgID}), 201).Body
	if team["organization_id"] != float64(orgID) {
		t.Errorf("team: %v", team)
	}
	if r := must(t, a.post(eda("teams/"), map[string]any{"name": uniq("team")}), 400); !strings.Contains(r.Raw, "organization_id") {
		t.Errorf("team without organization: %s", r.Raw)
	}
	me := must(t, a.get(eda("users/me/")), 200).Body
	if me["username"] != "admin" || me["is_superuser"] != true {
		t.Errorf("me: %v", me)
	}
	// EDA's user list shows fewer fields than a user's own page.
	if r := must(t, a.get(eda("users/?username=admin")), 200); r.count() != 1 || r.results()[0]["is_superuser"] != true {
		t.Errorf("users: %s", r.Raw)
	} else if _, ok := r.results()[0]["email"]; ok {
		t.Errorf("users list shows email: %s", r.Raw)
	}
	if u := must(t, a.get(eda(fmt.Sprintf("users/%v/", me["id"]))), 200).Body; u["email"] == nil || u["created_at"] == nil {
		t.Errorf("user: %v", u)
	}

	// EDA's names of awxnext's roles.
	rd := must(t, a.get(eda("role_definitions/?name="+url.QueryEscape("Organization Auditor"))), 200).results()
	if len(rd) != 1 || rd[0]["name"] != "Organization Auditor" {
		t.Fatalf("role definitions: %v", rd)
	}
	as := must(t, a.post(eda("role_team_assignments/"), map[string]any{"role_definition": rd[0]["id"], "team": team["id"], "object_id": orgID}), 201).Body
	if as["content_type"] != "shared.organization" || as["object_id"] != fmt.Sprint(orgID) {
		t.Errorf("assignment: %v", as)
	}
	q := fmt.Sprintf("role_team_assignments/?role_definition=%v&team=%v&page_size=200", rd[0]["id"], team["id"])
	if r := must(t, a.get(eda(q)), 200); r.count() != 1 {
		t.Errorf("assignments: %s", r.Raw)
	}
	must(t, a.delete(eda(fmt.Sprintf("role_team_assignments/%v/", as["id"]))), 204)
	if r := must(t, a.get(eda(q)), 200); r.count() != 0 {
		t.Errorf("assignments after delete: %s", r.Raw)
	}

	// Controller tokens are their user's.
	u, _ := newUser(t, a)
	name := uniq("tok")
	tok := must(t, u.post(eda("users/me/awx-tokens/"), map[string]any{"name": name, "token": "secret-value"}), 201).Body
	if strings.Contains(fmt.Sprint(tok), "secret-value") {
		t.Errorf("token shown: %v", tok)
	}
	if r := must(t, u.post(eda("users/me/awx-tokens/"), map[string]any{"name": name, "token": "x"}), 400); !strings.Contains(r.Raw, "already exists") {
		t.Errorf("duplicate token: %s", r.Raw)
	}
	must(t, u.post(eda("users/me/awx-tokens/"), map[string]any{"name": uniq("tok")}), 400)
	if r := must(t, u.get(eda("users/me/awx-tokens/?name="+name)), 200); r.count() != 1 {
		t.Errorf("tokens: %s", r.Raw)
	}
	if r := must(t, a.get(eda("users/me/awx-tokens/?name="+name)), 200); r.count() != 0 {
		t.Errorf("admin sees the user's token: %s", r.Raw)
	}
	tokURL := eda(fmt.Sprintf("users/me/awx-tokens/%v/", tok["id"]))
	must(t, a.get(tokURL), 404)
	must(t, a.delete(tokURL), 404)
	must(t, u.delete(tokURL), 204)
	must(t, u.get(tokURL), 404)
}

// Credential types, credentials and their input sources are awxnext's,
// with EDA's fields, filters and rules.
func TestEDACredentials(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	types := must(t, a.get(eda("credential-types/?name="+url.QueryEscape("Basic Event Stream"))), 200).results()
	if len(types) != 1 || types[0]["namespace"] != eventstreams.CredBasic || types[0]["managed"] != true {
		t.Fatalf("types: %v", types)
	}
	basic := num(types[0]["id"])
	if r := must(t, a.get(eda("credential-types/?name="+url.QueryEscape("Event-Driven Ansible Rule Engine"))), 200); r.count() != 1 {
		t.Errorf("rule engine type: %s", r.Raw)
	}
	ct := must(t, a.post(eda("credential-types/"), map[string]any{"name": uniq("type"), "inputs": map[string]any{
		"fields": []any{map[string]any{"id": "token", "label": "Token", "type": "string", "secret": true}}},
		"injectors": map[string]any{"extra_vars": map[string]any{"tok": "{{ token }}"}}}), 201).Body
	if ct["kind"] != "cloud" || ct["managed"] != false {
		t.Errorf("credential type: %v", ct)
	}
	if r := must(t, a.post(eda(fmt.Sprintf("credential-types/%v/test/", ct["id"])), map[string]any{}), 400); !strings.Contains(r.Raw, "inputs") {
		t.Errorf("test without inputs: %s", r.Raw)
	}

	name := uniq("cred")
	cred := must(t, a.post(eda("eda-credentials/"), map[string]any{"name": name, "credential_type_id": basic, "organization_id": org,
		"inputs": map[string]any{"username": "u", "password": "p"}}), 201).Body
	inputs := cred["inputs"].(map[string]any)
	if cred["credential_type_id"] != float64(basic) || cred["organization_id"] != float64(org) || inputs["password"] != "$encrypted$" ||
		cred["credential_type"].(map[string]any)["namespace"] != eventstreams.CredBasic {
		t.Errorf("credential: %v", cred)
	}
	// One credential of a name in an organization, whatever its type.
	if r := must(t, a.post(eda("eda-credentials/"), map[string]any{"name": name, "credential_type_id": ct["id"], "organization_id": org,
		"inputs": map[string]any{"token": "t"}}), 400); !strings.Contains(r.Raw, "already exists") {
		t.Errorf("duplicate: %s", r.Raw)
	}
	cp := must(t, a.post(eda(fmt.Sprintf("eda-credentials/%v/copy/", cred["id"])), map[string]any{"name": name + "-copy"}), 201).Body
	if cp["credential_type_id"] != float64(basic) || cp["organization_id"] != float64(org) {
		t.Errorf("copy: %v", cp)
	}
	must(t, a.post(eda(fmt.Sprintf("eda-credentials/%v/copy/", cred["id"])), map[string]any{"name": name + "-copy"}), 400)
	ns := eventstreams.CredBasic
	if r := must(t, a.get(eda("eda-credentials/?name="+name+"&credential_type__namespace="+ns)), 200); r.count() != 2 {
		t.Errorf("by namespace: %s", r.Raw)
	}
	if r := must(t, a.get(eda("eda-credentials/?name="+name+"&credential_type__namespace__not_in="+ns)), 200); r.count() != 0 {
		t.Errorf("not in namespace: %s", r.Raw)
	}
	upd := must(t, a.patch(eda(fmt.Sprintf("eda-credentials/%v/", cp["id"])), map[string]any{"description": "changed"}), 200).Body
	if upd["description"] != "changed" {
		t.Errorf("update: %v", upd)
	}

	// A stranger sees none of them; a known object is forbidden, not missing.
	u, _ := newUser(t, a)
	if r := must(t, u.get(eda("eda-credentials/?name="+name)), 200); r.count() != 0 {
		t.Errorf("stranger lists: %s", r.Raw)
	}
	must(t, u.get(eda(fmt.Sprintf("eda-credentials/%v/", cred["id"]))), 403)
	must(t, u.patch(eda(fmt.Sprintf("eda-credentials/%v/", cred["id"])), map[string]any{"description": "x"}), 403)
	must(t, u.delete(eda(fmt.Sprintf("eda-credentials/%v/", cred["id"]))), 403)
	must(t, u.get(eda("eda-credentials/999999999/")), 404)

	must(t, a.delete(eda(fmt.Sprintf("eda-credentials/%v/", cp["id"]))), 204)
	must(t, a.get(eda(fmt.Sprintf("eda-credentials/%v/", cp["id"]))), 404)
}

// Decision environments are execution environments; event streams are
// awxnext's, with EDA's UUID and headers.
func TestEDADecisionEnvironmentsAndStreams(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	de := must(t, a.post(eda("decision-environments/"), map[string]any{"name": uniq("de"), "image_url": "quay.io/ansible/ansible-rulebook:main",
		"organization_id": org}), 201).Body
	if de["pull_policy"] != "always" || de["image_url"] != "quay.io/ansible/ansible-rulebook:main" {
		t.Errorf("decision environment: %v", de)
	}
	if ee := must(t, a.get(fmt.Sprintf("execution_environments/%v/", de["id"])), 200).Body; ee["image"] != de["image_url"] || ee["pull"] != "always" {
		t.Errorf("execution environment: %v", ee)
	}
	if r := must(t, a.patch(eda(fmt.Sprintf("decision-environments/%v/", de["id"])), map[string]any{"pull_policy": "sometimes"}), 400); !strings.Contains(r.Raw, "pull_policy") {
		t.Errorf("bad pull policy: %s", r.Raw)
	}

	cred := streamCredential(t, a, org, eventstreams.CredBasic, map[string]any{"username": "u", "password": "p"})
	id := "8d6b3ab0-1f7e-4c3a-9d3e-0c7e8f4a6b21"
	body := map[string]any{"name": uniq("es"), "eda_credential_id": cred, "organization_id": org, "uuid": id,
		"additional_data_headers": "X-Request-Id,Authorization,Type", "test_mode": true}
	es := must(t, a.post(eda("event-streams/"), body), 201).Body
	if es["uuid"] != id || es["event_stream_type"] != "basic" || es["test_mode"] != true || !strings.HasSuffix(es["url"].(string), "/api/eda/v1/external_event_stream/"+id+"/post/") ||
		es["additional_data_headers"] != "X-Request-Id,Authorization,Type" {
		t.Fatalf("event stream: %v", es)
	}
	body["name"] = uniq("es")
	if r := must(t, a.post(eda("event-streams/"), body), 400); !strings.Contains(r.Raw, "already exists") {
		t.Errorf("duplicate uuid: %s", r.Raw)
	}
	body["uuid"] = "not-a-uuid"
	must(t, a.post(eda("event-streams/"), body), 400)
	must(t, a.patch(eda(fmt.Sprintf("event-streams/%v/", es["id"])), map[string]any{"uuid": "8d6b3ab0-1f7e-4c3a-9d3e-0c7e8f4a6b22"}), 400)

	// The listed headers become the event's attributes; a secret's never.
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	s := eventStream{id: num(es["id"]), path: "event_streams/" + id + "/post/"}
	if code, _, _ := s.post(t, `{"a":1}`, headers("Authorization", basic, "Content-Type", "application/json", "X-Request-Id", "r-1", "Type", "x")); code != 202 {
		t.Fatalf("post: %d", code)
	}
	// EDA's receiver path takes them too.
	req, _ := http.NewRequest("POST", srv.URL+"/api/eda/v1/external_event_stream/"+id+"/post/", strings.NewReader(`{"a":2}`))
	req.Header.Set("Authorization", basic)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("post to EDA's path: %d", res.StatusCode)
	}
	evs := waitEvents(t, a, s, 2)
	ev := fmt.Sprint(evs[0])
	if !strings.Contains(ev, "xrequestid:r-1") || strings.Contains(ev, "Basic") || strings.Contains(ev, "authorization") {
		t.Errorf("event: %v", evs[0])
	}
	if r := must(t, a.get(eda("event-streams/?name="+es["name"].(string))), 200); r.count() != 1 {
		t.Errorf("list: %s", r.Raw)
	}
	must(t, a.delete(eda(fmt.Sprintf("event-streams/%v/", es["id"]))), 204)
}

// An EDA project syncs as awxnext's, and its rulebooks are the
// project's; an activation runs a copy of one, with its own connectors
// and rules, turned on and off, restarted and copied.
func TestEDAActivations(t *testing.T) {
	requireConnectors(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	orgName := must(t, a.get(fmt.Sprintf("organizations/%d/", org)), 200).Body["name"].(string)
	jtName := uniq("deploy")
	rulebook := func(limit int) string {
		return fmt.Sprintf(`- name: Ticks
  hosts: all
  sources:
    - name: ticks
      test.fake.tick:
        delay: 0.2
  rules:
    - name: Deploy
      condition: event.i == %d
      action:
        run_job_template:
          name: %[2]s
          organization: %[3]s
- name: Hooks
  hosts: all
  sources:
    - ansible.eda.webhook:
        port: 5000
  rules:
    - name: Hooked
      condition: event.payload.go == true
      action:
        run_job_template:
          name: %[2]s
          organization: %[3]s
`, limit, jtName, orgName)
	}
	dir := gitRepo(t, map[string]string{"site.yml": helloPlaybook, "rulebooks/ticks.yml": rulebook(3)})
	p := must(t, a.post(eda("projects/"), map[string]any{"name": uniq("proj"), "url": dir, "organization_id": org,
		"update_revision_on_launch": true, "scm_update_cache_timeout": 60}), 201).Body
	pid := num(p["id"])
	if p["scm_type"] != "git" || p["url"] != dir || p["verify_ssl"] != true || p["proxy"] != "" || p["update_revision_on_launch"] != true ||
		p["scm_update_cache_timeout"] != float64(60) {
		t.Errorf("project: %v", p)
	}
	p = waitEDAProject(t, a, pid)
	if p["git_hash"] == "" || p["last_synced_at"] == nil || p["import_error"] != nil || p["modified_at"] == p["created_at"] {
		t.Errorf("synced project: %v", p)
	}
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	jt := must(t, a.post("job_templates/", map[string]any{"name": jtName, "project": pid, "inventory": inv, "playbook": "site.yml",
		"ask_variables_on_launch": true}), 201).id()
	// A sync imports the rulebooks again: now their rules find the template.
	if r := must(t, a.post(eda(fmt.Sprintf("projects/%d/sync/", pid)), nil), 202); r.Body["id"] != float64(pid) {
		t.Errorf("sync: %s", r.Raw)
	}
	waitEDAProject(t, a, pid)

	rbs := must(t, a.get(eda(fmt.Sprintf("rulebooks/?project_id=%d&name=ticks", pid))), 200).results()
	if len(rbs) != 1 || rbs[0]["name"] != "ticks.yml" || rbs[0]["organization_id"] != float64(org) {
		t.Fatalf("rulebooks: %v", rbs)
	}
	rb := num(rbs[0]["id"])
	sources := must(t, a.get(eda(fmt.Sprintf("rulebooks/%d/sources/?name=ticks.yml", rb))), 200).results()
	sum := sha256.Sum256([]byte(rbs[0]["rulesets"].(string)))
	if len(sources) != 2 || sources[0]["name"] != "ticks" || sources[1]["name"] != "__SOURCE_2" || sources[0]["rulebook_hash"] != hex.EncodeToString(sum[:]) ||
		!strings.Contains(sources[0]["source_info"].(string), "test.fake.tick") {
		t.Fatalf("sources: %v", sources)
	}
	de := must(t, a.post(eda("decision-environments/"), map[string]any{"name": uniq("de"), "image_url": "quay.io/ansible/ansible-rulebook:main",
		"organization_id": org}), 201).Body
	cred := streamCredential(t, a, org, eventstreams.CredBasic, map[string]any{"username": "u", "password": "p"})

	name := uniq("act")
	body := map[string]any{"name": name, "rulebook_id": rb, "decision_environment_id": de["id"], "organization_id": org,
		"eda_credentials": []any{cred}, "extra_var": "greeting: hello\n", "restart_policy": "never", "log_level": "info", "is_enabled": true}
	act := must(t, a.post(eda("activations/"), body), 201).Body
	actID := num(act["id"])
	if act["rulebook_id"] != float64(rb) || act["project_id"] != float64(pid) || act["is_enabled"] != true || act["rulebook_name"] != "ticks.yml" ||
		act["restart_policy"] != "never" || act["log_level"] != "info" || act["extra_var"] != "greeting: hello\n" || act["log_tracking_id"] == "" ||
		act["rules_count"] != float64(2) || len(act["eda_credentials"].([]any)) != 1 {
		t.Fatalf("activation: %v\nreport: %s", act, a.get(fmt.Sprintf("rulebooks/%d/", num(act["id"]))).Raw)
	}
	actURL := eda(fmt.Sprintf("activations/%d/", actID))
	// The activation's rulebook is its own: not one of the project's for EDA.
	if r := must(t, a.get(eda(fmt.Sprintf("rulebooks/?project_id=%d", pid))), 200); r.count() != 1 {
		t.Errorf("rulebooks with an activation: %s", r.Raw)
	}
	own := must(t, a.get(fmt.Sprintf("rulebooks/%d/", actID)), 200).Body
	if own["name"] != name || own["variables"].(map[string]any)["greeting"] != "hello" {
		t.Errorf("activation's rulebook: %v", own)
	}
	conns := rulebookObjects(t, a, actID, "event_connectors")
	conn := conns["Ticks / ticks"]
	if len(conns) != 1 || conn["enabled"] != true || conn["execution_environment"] != de["id"] || conn["restart_policy"] != "never" ||
		fmt.Sprint(conn["credentials"]) != fmt.Sprintf("[%d]", cred) {
		t.Fatalf("connectors: %v", conns)
	}
	rules := rulebookObjects(t, a, actID, "event_rules")
	rule := rules["Ticks / Deploy"]
	if len(rules) != 2 || rule["enabled"] != true || rule["mode"] != "auto" || rule["owner"] == nil || rule["unified_job_template"] != float64(jt) {
		t.Fatalf("rules: %v", rules)
	}
	// The project's rulebook stays as its import left it.
	if r := rulebookObjects(t, a, rb, "event_rules")["Ticks / Deploy"]; r["enabled"] != false || r["mode"] != "shadow" {
		t.Errorf("project's rule: %v", r)
	}
	waitActivation(t, a, actID, "running")
	waitFor(t, "the rule's launch", func() bool {
		return must(t, a.get(fmt.Sprintf("jobs/?unified_job_template=%d", jt)), 200).count() > 0
	})
	if r := must(t, a.get(eda("activations/?name="+name)), 200); r.count() != 1 {
		t.Errorf("list: %s", r.Raw)
	}
	if r := must(t, a.get(eda(fmt.Sprintf("activations/?decision_environment_id=%v&name=%s", de["id"], name))), 200); r.count() != 1 {
		t.Errorf("by decision environment: %s", r.Raw)
	}

	// Restart, then off and on.
	must(t, a.post(actURL+"restart/", map[string]any{"id": actID}), 204)
	got := must(t, a.get(actURL), 200).Body
	if got["restart_count"] != float64(1) || (got["status"] != "starting" && got["status"] != "running") {
		t.Errorf("restarted: %v", got)
	}
	waitActivation(t, a, actID, "running")
	must(t, a.post(actURL+"disable/", nil), 204)
	got = must(t, a.get(actURL), 200).Body
	if got["is_enabled"] != false || got["status"] != "stopped" {
		t.Errorf("disabled: %v", got)
	}
	if r := rulebookObjects(t, a, actID, "event_rules")["Ticks / Deploy"]; r["enabled"] != false {
		t.Errorf("rule after disable: %v", r)
	}
	if c := rulebookObjects(t, a, actID, "event_connectors")["Ticks / ticks"]; c["enabled"] != false {
		t.Errorf("connector after disable: %v", c)
	}
	must(t, a.post(actURL+"restart/", nil), 400)

	// A copy is off, with the same settings.
	cp := must(t, a.post(actURL+"copy/", map[string]any{"name": name + "_Copy"}), 201).Body
	for _, f := range []string{"rulebook_id", "decision_environment_id", "project_id", "extra_var", "restart_policy", "log_level", "eda_credentials",
		"source_mappings", "rules_count"} {
		if fmt.Sprint(cp[f]) != fmt.Sprint(got[f]) {
			t.Errorf("copy's %s: %v, want %v", f, cp[f], got[f])
		}
	}
	if cp["is_enabled"] != false {
		t.Errorf("copy on: %v", cp)
	}
	if r := must(t, a.post(actURL+"copy/", map[string]any{"name": name + "_Copy"}), 400); !strings.Contains(r.Raw, "Activation with name "+name+"_Copy already exists") {
		t.Errorf("duplicate copy: %s", r.Raw)
	}
	if r := must(t, a.post(eda("activations/"), body), 400); !strings.Contains(r.Raw, "already exists") {
		t.Errorf("duplicate: %s", r.Raw)
	}
	must(t, a.delete(eda(fmt.Sprintf("activations/%v/", cp["id"]))), 204)

	// Mapping a source to an event stream: its events reach the rules
	// instead of the source's own stream.
	es := must(t, a.post(eda("event-streams/"), map[string]any{"name": uniq("es"), "eda_credential_id": cred, "organization_id": org}), 201).Body
	mapping := func(source, hash string) string {
		b, _ := yaml.Marshal([]map[string]any{{"source_name": source, "rulebook_hash": hash, "event_stream_name": es["name"], "event_stream_id": es["id"]}})
		return string(b)
	}
	good := mapping("__SOURCE_2", sources[0]["rulebook_hash"].(string))
	upd := must(t, a.patch(actURL, map[string]any{"source_mappings": good, "rulebook_id": rb, "name": name + "_Modified"}), 200).Body
	if upd["source_mappings"] != good || upd["name"] != name+"_Modified" || len(upd["event_streams"].([]any)) != 1 {
		t.Errorf("mapped: %v", upd)
	}
	if streams := rulebookObjects(t, a, actID, "event_streams"); len(streams) != 1 {
		t.Errorf("activation's own streams: %v", streams)
	}
	if r := rulebookObjects(t, a, actID, "event_rules")["Hooks / Hooked"]; fmt.Sprint(r["streams"]) != fmt.Sprintf("[%v]", es["id"]) || r["enabled"] != false {
		t.Errorf("rule doesn't read the mapped stream: %v", r)
	}
	if r := must(t, a.patch(actURL, map[string]any{"source_mappings": mapping("nope", sources[0]["rulebook_hash"].(string))}), 400); !strings.Contains(r.Raw, "does not exist") {
		t.Errorf("unknown source: %s", r.Raw)
	}
	if r := must(t, a.patch(actURL, map[string]any{"source_mappings": mapping("ticks", "0")}), 400); !strings.Contains(r.Raw, "changed") {
		t.Errorf("stale hash: %s", r.Raw)
	}
	if r := must(t, a.patch(actURL, map[string]any{"extra_var": "- a list"}), 400); !strings.Contains(r.Raw, "extra_var") {
		t.Errorf("list extra_var: %s", r.Raw)
	}

	// A stranger can't see it; deleting it deletes its rulebook and objects.
	u, _ := newUser(t, a)
	must(t, u.get(actURL), 403)
	must(t, u.post(actURL+"enable/", nil), 403)
	if r := must(t, u.get(eda("activations/?name="+name)), 200); r.count() != 0 {
		t.Errorf("stranger lists: %s", r.Raw)
	}
	connID := conn["id"]
	must(t, a.delete(actURL), 204)
	must(t, a.get(actURL), 404)
	must(t, a.get(fmt.Sprintf("rulebooks/%d/", actID)), 404)
	must(t, a.get(fmt.Sprintf("event_connectors/%v/", connID)), 404)
	must(t, a.get(fmt.Sprintf("event_streams/%v/", es["id"])), 200)

	// An activation that restarts on project updates runs the new rulebook.
	body["name"], body["restart_on_project_update"], body["extra_var"] = uniq("act"), true, nil
	again := must(t, a.post(eda("activations/"), body), 201).Body
	againURL := eda(fmt.Sprintf("activations/%v/", again["id"]))
	if again["extra_var"] != nil {
		t.Errorf("extra_var: %v", again["extra_var"])
	}
	stays := must(t, a.post(eda("activations/"), map[string]any{"name": uniq("act"), "rulebook_id": rb, "decision_environment_id": de["id"],
		"organization_id": org, "is_enabled": false}), 201).Body
	commitFiles(t, dir, map[string]string{"rulebooks/ticks.yml": rulebook(5)}, "five")
	must(t, a.post(eda(fmt.Sprintf("projects/%d/sync/", pid)), nil), 202)
	waitFor(t, "the activation's new rulebook", func() bool {
		return strings.Contains(must(t, a.get(fmt.Sprintf("rulebooks/%v/", again["id"])), 200).Body["rulesets"].(string), "event.i == 5")
	})
	if got := must(t, a.get(againURL), 200).Body; got["restart_count"] != float64(1) {
		t.Errorf("not restarted: %v", got)
	}
	if r := rulebookObjects(t, a, num(again["id"]), "event_rules")["Ticks / Deploy"]; !strings.Contains(fmt.Sprint(r["filter"]), "5") || r["enabled"] != true {
		t.Errorf("rule after the update: %v", r)
	}
	if rs := must(t, a.get(fmt.Sprintf("rulebooks/%v/", stays["id"])), 200).Body["rulesets"].(string); !strings.Contains(rs, "event.i == 3") {
		t.Errorf("an activation without restart_on_project_update changed: %s", rs)
	}

	// An organization's admin runs one as themselves: its rules act as them.
	orgAdmin, adminID := newUser(t, a)
	rd := must(t, a.get(eda("role_definitions/?name="+url.QueryEscape("Organization Admin"))), 200).results()
	if len(rd) != 1 {
		t.Fatalf("role definitions: %v", rd)
	}
	must(t, a.post(eda("role_user_assignments/"), map[string]any{"role_definition": rd[0]["id"], "user": adminID, "object_id": org}), 201)
	mine := must(t, orgAdmin.post(eda("activations/"), map[string]any{"name": uniq("act"), "rulebook_id": rb, "decision_environment_id": de["id"],
		"organization_id": org, "is_enabled": false}), 201).Body
	if r := rulebookObjects(t, a, num(mine["id"]), "event_rules")["Ticks / Deploy"]; r["enabled"] != false {
		t.Errorf("disabled activation's rule: %v", r)
	}
	must(t, orgAdmin.post(eda(fmt.Sprintf("activations/%v/enable/", mine["id"])), nil), 204)
	if r := rulebookObjects(t, a, num(mine["id"]), "event_rules")["Ticks / Deploy"]; r["enabled"] != true || r["owner"] != float64(adminID) {
		t.Errorf("enabled activation's rule: %v", r)
	}
	must(t, u.patch(eda(fmt.Sprintf("activations/%v/", mine["id"])), map[string]any{"description": "x"}), 403)
	must(t, orgAdmin.post(eda(fmt.Sprintf("activations/%v/disable/", mine["id"])), nil), 204)

	// Deleting the project deletes its activations.
	must(t, a.delete(eda(fmt.Sprintf("projects/%d/", pid))), 204)
	waitFor(t, "the project's activations to go", func() bool { return a.get(againURL).Status == 404 })
}

// A project's proxy is masked, and git goes through it.
func TestEDAProjectProxy(t *testing.T) {
	slow(t)
	type hit struct{ host, auth string }
	hits := make(chan hit, 10)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Proxy-Authorization")
		if auth == "" {
			w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
			http.Error(w, "who", http.StatusProxyAuthRequired)
			return
		}
		select {
		case hits <- hit{r.Host, auth}:
		default:
		}
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	host := strings.TrimPrefix(proxy.URL, "http://")
	a := adminClient(t)
	org := newOrg(t, a)
	p := must(t, a.post(eda("projects/"), map[string]any{"name": uniq("proj"), "url": "http://git.example.invalid/repo.git", "organization_id": org,
		"proxy": "http://user:s3cret@" + host, "verify_ssl": false}), 201).Body
	if p["proxy"] != "http://user:$encrypted$@"+host || p["verify_ssl"] != false || strings.Contains(fmt.Sprint(p), "s3cret") {
		t.Fatalf("project: %v", p)
	}
	pid := num(p["id"])
	select {
	case h := <-hits:
		if h.host != "git.example.invalid" || h.auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:s3cret")) {
			t.Errorf("proxied request: %+v", h)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("git didn't go through the proxy")
	}
	got := waitEDAProjectState(t, a, pid, "failed")
	if strings.Contains(fmt.Sprint(got["import_error"]), "s3cret") {
		t.Errorf("import_error: %v", got["import_error"])
	}
	if r := must(t, a.patch(eda(fmt.Sprintf("projects/%d/", pid)), map[string]any{"proxy": "http://user:$encrypted$@other:9"}), 400); !strings.Contains(r.Raw, "proxy") {
		t.Errorf("placeholder proxy: %s", r.Raw)
	}
	must(t, a.patch(eda(fmt.Sprintf("projects/%d/", pid)), map[string]any{"proxy": "ftp://x"}), 400)
	// The masked proxy sent back is no change.
	if r := must(t, a.patch(eda(fmt.Sprintf("projects/%d/", pid)), map[string]any{"proxy": p["proxy"]}), 200); r.Body["proxy"] != p["proxy"] {
		t.Errorf("masked proxy: %s", r.Raw)
	}
	var stored string
	if err := app.DB.QueryRow(t.Context(), "SELECT scm_proxy FROM unified_job_templates WHERE id = $1", pid).Scan(&stored); err != nil || strings.Contains(stored, "s3cret") {
		t.Errorf("stored proxy: %q, %v", stored, err)
	}
	// Without a proxy, the next sync goes straight to the repository.
	allowFileURLs(t, a)
	dir := gitRepo(t, map[string]string{"site.yml": helloPlaybook})
	if r := must(t, a.patch(eda(fmt.Sprintf("projects/%d/", pid)), map[string]any{"proxy": "", "url": dir}), 200); r.Body["proxy"] != "" {
		t.Errorf("cleared proxy: %s", r.Raw)
	}
	waitEDAProject(t, a, pid)
}

func waitEDAProject(t *testing.T, a *client, id int64) map[string]any {
	t.Helper()
	return waitEDAProjectState(t, a, id, "completed")
}

func waitEDAProjectState(t *testing.T, a *client, id int64, state string) map[string]any {
	t.Helper()
	var p map[string]any
	waitFor(t, "the project's import", func() bool {
		p = must(t, a.get(eda(fmt.Sprintf("projects/%d/", id))), 200).Body
		if p["import_state"] == "failed" && state != "failed" {
			t.Fatalf("project import failed: %v", p)
		}
		return p["import_state"] == state
	})
	return p
}

func waitActivation(t *testing.T, a *client, id int64, status string) {
	t.Helper()
	var got map[string]any
	waitFor(t, "activation "+status, func() bool {
		got = must(t, a.get(eda(fmt.Sprintf("activations/%d/", id))), 200).Body
		return got["status"] == status
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

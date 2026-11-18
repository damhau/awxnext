package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const provisionPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.set_stats:
        data:
          host: "db.{{ name }}.internal"
          port: 5432
          db_user: "{{ name }}_app"
          db_password: "Init-{{ name }}-Secret"
          scratch: not declared
      run_once: true
`

const rotatePlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.copy:
        content: "{{ db_password }}"
        dest: "OUT/rotate-{{ awx_job_id }}"
      run_once: true
    - ansible.builtin.set_stats:
        data:
          db_password: "Rotated-{{ db_user }}-Secret"
      run_once: true
`

const resizePlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.set_stats:
        data:
          size: "{{ size }}"
      run_once: true
`

const teardownPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.copy:
        content: "{{ db_password }} {{ host }}"
        dest: "OUT/teardown-{{ awx_job_id }}"
      run_once: true
    - ansible.builtin.stat:
        path: OUT/fail-teardown
      register: flag
      run_once: true
    - ansible.builtin.set_stats:
        data:
          left_behind: ["lb-{{ db_user }}"]
      run_once: true
      when: flag.stat.exists
    - ansible.builtin.fail:
        msg: the load balancer is still in use
      run_once: true
      when: flag.stat.exists
`

func waitRequest(t *testing.T, a *client, id any) map[string]any {
	t.Helper()
	var body map[string]any
	waitFor(t, fmt.Sprintf("request %v", id), func() bool {
		body = must(t, a.get(fmt.Sprintf("catalog_requests/%v/", id)), 200).Body
		return slices.Contains([]string{"successful", "failed", "canceled", "denied"}, fmt.Sprint(body["status"]))
	})
	return body
}

// connectionText renders a deployment's connection as label=value, with
// [secret] and [no copy] marks.
func connectionText(d map[string]any) string {
	var out []string
	for _, x := range d["connection"].([]any) {
		e := x.(map[string]any)
		s := fmt.Sprintf("%v=%v", e["label"], e["value"])
		if e["secret"] == true {
			s += " [secret]"
		}
		if e["copy"] == false {
			s += " [no copy]"
		}
		if e["error"] != nil {
			s += fmt.Sprintf(" [error %v]", e["error"])
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

// An item's request provisions a deployment with the run's declared
// outputs, laid out as the item says; its owners reveal its secrets (each
// showing logged), run its actions through the item's rules, hand it over
// and delete it, and a failed teardown leaves it flagged.
func TestDeployments(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	out := t.TempDir()
	repo := gitRepo(t, map[string]string{
		"provision.yml": provisionPlaybook,
		"rotate.yml":    strings.ReplaceAll(rotatePlaybook, "OUT", out),
		"resize.yml":    resizePlaybook,
		"teardown.yml":  strings.ReplaceAll(teardownPlaybook, "OUT", out),
	})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("deploy"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	str := func(name string) map[string]any { return map[string]any{"name": name, "type": "string"} }
	prov := f.template(t, "provision.yml", map[string]any{"survey_enabled": true, "outputs": []any{str("host"),
		map[string]any{"name": "port", "type": "integer"}, str("db_user"), map[string]any{"name": "db_password", "type": "string", "secret": true}}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", prov), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Name", "variable": "name", "type": "text", "required": true},
	}}), 200)
	rotate := f.template(t, "rotate.yml", map[string]any{"outputs": []any{map[string]any{"name": "db_password", "type": "string", "secret": true}}})
	resize := f.template(t, "resize.yml", map[string]any{"survey_enabled": true, "outputs": []any{str("size")}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", resize), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Size", "variable": "size", "type": "multiplechoice", "choices": []any{"medium", "large"}, "required": true},
	}}), 200)
	teardown := f.template(t, "teardown.yml", nil)

	layout := []any{
		map[string]any{"label": "Host", "value": "host"},
		map[string]any{"label": "Port", "value": "port"},
		map[string]any{"label": "User", "value": "outputs.db_user", "copy": false},
		map[string]any{"label": "Password", "value": "db_password"},
		map[string]any{"label": "Connection string", "value": `"postgresql://" + db_user + ":" + db_password + "@" + host + ":" + string(port) + "/app"`},
	}
	actions := []any{
		map[string]any{"id": "rotate", "name": "Rotate the password", "description": "A new password, the old one stops working", "template": rotate},
		map[string]any{"id": "resize", "name": "Resize", "description": "Large needs your team lead", "template": resize},
	}
	for what, body := range map[string]map[string]any{
		"a layout on an item that runs once":  {"connection": layout},
		"an action named as a request's kind": {"kind": "deployment", "actions": []any{map[string]any{"id": "delete", "name": "x", "template": rotate}}},
		"an invalid value":                    {"kind": "deployment", "connection": []any{map[string]any{"label": "Host", "value": "host +"}}},
		"two entries with one label":          {"kind": "deployment", "connection": []any{layout[0], layout[0]}},
	} {
		body["name"], body["unified_job_template"] = uniq("bad"), prov
		if r := a.post("catalog_items/", body); r.Status != 400 {
			t.Errorf("%s: %d %s", what, r.Status, r.Raw)
		}
	}
	item := must(t, a.post("catalog_items/", map[string]any{
		"name": uniq("db"), "unified_job_template": prov, "kind": "deployment", "default_lifetime_days": 7,
		"deployment_name": `answers.name + "-db"`, "connection": layout, "actions": actions, "teardown_template": teardown,
		"approval_rules": []any{map[string]any{"name": "Large", "condition": `kind == "resize" && size == "large"`, "approvers": "team_admins"}},
	}), 201).id()

	// The request provisions it, with the declared outputs only.
	req := waitRequest(t, a, f.request(t, f.requester, item, map[string]any{"name": "orders"})["id"])
	if req["status"] != "successful" {
		t.Fatalf("provisioning: %v", req)
	}
	waitFor(t, "the deployment", func() bool { return f.get(t, req)["deployment"] != nil })
	dep := fmt.Sprintf("deployments/%v/", f.get(t, req)["deployment"])
	d := must(t, f.requester.get(dep), 200)
	if d.Body["name"] != "orders-db" || d.Body["owner"] != float64(f.reqID) || d.Body["team"] != float64(f.team) || d.Body["status"] != "active" ||
		d.Body["expires_at"] == nil || d.Body["request"] != req["id"] {
		t.Errorf("deployment: %s", d.Raw)
	}
	if got := fmt.Sprint(d.Body["outputs"]); got != "map[db_password:$encrypted$ db_user:orders_app host:db.orders.internal port:5432]" {
		t.Errorf("outputs: %s", got)
	}
	want := "Host=db.orders.internal; Port=5432; User=orders_app [no copy]; Password=$encrypted$ [secret]; Connection string=$encrypted$ [secret]"
	if got := connectionText(d.Body); got != want {
		t.Errorf("connection:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(d.Raw, "Init-orders-Secret") {
		t.Errorf("the secret is in the deployment: %s", d.Raw)
	}
	if n := must(t, f.requester.get(dep+"requests/"), 200).count(); n != 1 {
		t.Errorf("its requests: %d", n)
	}
	must(t, f.p1.get(dep), 403)

	// Without a layout, every output shows under its name.
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"connection": []any{}}), 200)
	if got := connectionText(must(t, f.requester.get(dep), 200).Body); got != "db_password=$encrypted$ [secret]; db_user=orders_app; host=db.orders.internal; port=5432" {
		t.Errorf("default connection: %s", got)
	}
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"connection": layout}), 200)

	// Its owners see a secret; each showing is logged, without the value.
	if r := f.admin.post(dep+"reveal/", map[string]any{"label": "Password"}); r.Status != 403 {
		t.Errorf("an organization admin revealed it: %d", r.Status)
	}
	must(t, f.requester.post(dep+"reveal/", map[string]any{"label": "Nope"}), 400)
	reveal := func(label string) string {
		t.Helper()
		return fmt.Sprint(must(t, f.requester.post(dep+"reveal/", map[string]any{"label": label}), 200).Body["value"])
	}
	if got := reveal("Password"); got != "Init-orders-Secret" {
		t.Errorf("password: %q", got)
	}
	if got := reveal("Connection string"); got != "postgresql://orders_app:Init-orders-Secret@db.orders.internal:5432/app" {
		t.Errorf("connection string: %q", got)
	}
	activity := must(t, a.get(dep+"activity_stream/?operation=update"), 200)
	shown := 0
	for _, e := range activity.results() {
		if fmt.Sprint(e["changes"].(map[string]any)["revealed"]) != "" && e["summary_fields"].(map[string]any)["actor"].(map[string]any)["username"] == f.requester.user {
			shown++
		}
	}
	if shown != 2 || strings.Contains(activity.Raw, "Init-orders-Secret") {
		t.Errorf("reveals logged: %d\n%s", shown, activity.Raw)
	}

	// A day-2 action gets the outputs and updates them.
	list := must(t, f.requester.get(dep+"actions/"), 200).Body["actions"].([]any)
	if len(list) != 2 || list[1].(map[string]any)["name"] != "Resize" || fmt.Sprint(variables(list[1].(map[string]any)["questions"])) != "[size]" {
		t.Errorf("actions: %v", list)
	}
	must(t, f.requester.post(dep+"actions/", map[string]any{"action": "nope"}), 400)
	must(t, f.p1.post(dep+"actions/", map[string]any{"action": "rotate"}), 403)
	var stored string
	app.DB.QueryRow(context.Background(), "SELECT outputs->>'db_password' FROM deployments WHERE id = $1", d.Body["id"]).Scan(&stored)
	rot := must(t, f.requester.post(dep+"actions/", map[string]any{"action": "rotate", "reason": "leaked"}), 201).Body
	if rot["kind"] != "rotate" || rot["deployment"] != d.Body["id"] {
		t.Errorf("action request: %v", rot)
	}
	if r := f.requester.post(dep+"actions/", map[string]any{"action": "resize", "answers": map[string]any{"size": "medium"}}); r.Status != 400 ||
		!strings.Contains(r.Raw, "hasn't finished") {
		t.Errorf("a second action while one runs: %d %s", r.Status, r.Raw)
	}
	if rot = waitRequest(t, a, rot["id"]); rot["status"] != "successful" {
		t.Fatalf("rotate: %v", rot)
	}
	waitFor(t, "the rotated password", func() bool {
		var now string
		app.DB.QueryRow(context.Background(), "SELECT outputs->>'db_password' FROM deployments WHERE id = $1", d.Body["id"]).Scan(&now)
		return now != stored && strings.HasPrefix(now, "$encrypted$v1$")
	})
	if got := readOut(t, out, fmt.Sprintf("rotate-%v", rot["job"])); got != "Init-orders-Secret" {
		t.Errorf("the action got %q", got)
	}
	if got := reveal("Password"); got != "Rotated-orders_app-Secret" {
		t.Errorf("rotated password: %q", got)
	}

	// An action goes through the item's rules.
	rs := must(t, f.requester.post(dep+"actions/", map[string]any{"action": "resize", "answers": map[string]any{"size": "large"}}), 201).Body
	if rs["status"] != "awaiting_approval" || len(approvalIDs(rs)) != 1 {
		t.Fatalf("a large resize: %v", rs)
	}
	must(t, approve(f.lead, approvalIDs(rs)[0], ""), 204)
	if rs = waitRequest(t, a, rs["id"]); rs["status"] != "successful" {
		t.Fatalf("resize: %v", rs)
	}
	waitFor(t, "the new size", func() bool {
		return must(t, a.get(dep), 200).Body["outputs"].(map[string]any)["size"] == "large"
	})

	// It is handed over to whoever may request the item, who is told.
	s := startSMTP(t, &smtpServer{})
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"notification_template": newEmailTemplate(t, a, f.org, emailConfig(s.port, nil), nil)}), 200)
	must(t, f.requester.post(dep+"hand_over/", map[string]any{"user": f.p1ID}), 400)
	choices := func(c *client) (users, teams []float64) {
		b := must(t, c.get(dep+"hand_over/"), 200).Body
		for _, x := range b["users"].([]any) {
			users = append(users, x.(map[string]any)["id"].(float64))
		}
		for _, x := range b["teams"].([]any) {
			teams = append(teams, x.(map[string]any)["id"].(float64))
		}
		return users, teams
	}
	if users, _ := choices(f.requester); slices.Contains(users, float64(f.p1ID)) || slices.Contains(users, float64(f.reqID)) {
		t.Errorf("hand-over choices before the grant: %v", users)
	}
	must(t, f.p2.get(dep+"hand_over/"), 403)
	grant(t, a, "CatalogItem Requester", f.p1ID, item)
	if users, teams := choices(f.requester); !slices.Contains(users, float64(f.p1ID)) || slices.Contains(teams, float64(f.platform)) {
		t.Errorf("hand-over choices: %v %v", users, teams)
	}
	h := must(t, f.requester.post(dep+"hand_over/", map[string]any{"user": f.p1ID}), 200).Body
	if h["owner"] != float64(f.p1ID) || h["team"] != float64(f.team) {
		t.Errorf("handed over: %v", h)
	}
	if m := s.next(t); strings.Join(m.To, ",") != f.p1.user+"@example.com" || !strings.Contains(m.subject(), "handed over to you") ||
		!strings.Contains(m.Body, fmt.Sprintf("/portal/deployments/%v", d.Body["id"])) {
		t.Errorf("hand-over email: %v %q", m.To, m.subject())
	}
	must(t, f.p1.post(dep+"hand_over/", map[string]any{"team": f.platform}), 400)
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "CatalogItem Requester"), "team": f.platform, "object_id": item}), 201)
	if users, teams := choices(f.p1); !slices.Contains(teams, float64(f.platform)) || slices.Contains(users, float64(f.p1ID)) {
		t.Errorf("hand-over choices with the team's grant: %v %v", users, teams)
	}
	h = must(t, f.p1.post(dep+"hand_over/", map[string]any{"team": f.platform}), 200).Body
	if h["owner"] != nil || h["team"] != float64(f.platform) {
		t.Errorf("handed over to a team: %v", h)
	}
	if m := s.next(t); strings.Join(m.To, ",") != f.p2.user+"@example.com" {
		t.Errorf("the team's email: %v", m.To)
	}
	must(t, f.requester.get(dep), 403)

	// A failed teardown leaves it flagged, with what is left.
	flag := filepath.Join(out, "fail-teardown")
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, f.p2.delete(dep), 202)
	var gone map[string]any
	waitFor(t, "the failed teardown", func() bool {
		gone = must(t, a.get(dep), 200).Body
		return gone["status"] == "delete_failed"
	})
	if gone["explanation"] == "" || fmt.Sprint(gone["left_behind"]) != "[lb-orders_app]" || gone["deleted_at"] != nil {
		t.Errorf("flagged: %v", gone)
	}
	if m := s.next(t); !strings.Contains(m.subject(), "couldn't be deleted") {
		t.Errorf("the failure's email: %q", m.subject())
	}
	del := must(t, a.get(dep+"requests/?kind=delete"), 200).results()
	if len(del) != 1 || del[0]["status"] != "failed" {
		t.Fatalf("the delete request: %v", del)
	}
	if got := readOut(t, out, fmt.Sprintf("teardown-%v", del[0]["job"])); got != "Rotated-orders_app-Secret db.orders.internal" {
		t.Errorf("the teardown got %q", got)
	}
	must(t, f.p1.post(dep+"actions/", map[string]any{"action": "rotate"}), 400)

	// Deleting it again retries.
	os.Remove(flag)
	must(t, f.p2.delete(dep), 202)
	waitFor(t, "the deletion", func() bool {
		gone = must(t, a.get(dep), 200).Body
		return gone["status"] == "deleted"
	})
	if gone["deleted_at"] == nil || gone["left_behind"] != nil {
		t.Errorf("deleted: %v", gone)
	}
	must(t, f.p2.delete(dep), 400)
	if n := must(t, a.get(dep+"requests/"), 200).count(); n != 5 {
		t.Errorf("its requests: %d", n)
	}
}

// Without a teardown template, a deployment is deleted at once.
func TestDeploymentWithoutTeardown(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("deploy"), "organization": f.org, "scm_type": "git",
		"scm_url": gitRepo(t, map[string]string{"provision.yml": provisionPlaybook})}), 201).id()
	f.waitProject(t)
	prov := f.template(t, "provision.yml", map[string]any{"survey_enabled": true, "outputs": []any{map[string]any{"name": "host", "type": "string"}}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", prov), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Name", "variable": "name", "type": "text", "required": true},
	}}), 200)
	item := must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": prov, "kind": "deployment"}), 201).id()
	req := waitRequest(t, a, f.request(t, f.requester, item, map[string]any{"name": "web"})["id"])
	waitFor(t, "the deployment", func() bool { return f.get(t, req)["deployment"] != nil })
	dep := fmt.Sprintf("deployments/%v/", f.get(t, req)["deployment"])
	d := must(t, f.requester.get(dep), 200).Body
	if !strings.HasPrefix(fmt.Sprint(d["name"]), fmt.Sprint(must(t, a.get(fmt.Sprintf("catalog_items/%d/", item)), 200).Body["name"])) || d["expires_at"] != nil {
		t.Errorf("deployment: %v", d)
	}
	if got := connectionText(d); got != "host=db.web.internal" {
		t.Errorf("connection: %s", got)
	}
	must(t, f.requester.patch(dep, map[string]any{"name": "web-1", "description": "the front"}), 200)
	must(t, f.p1.delete(dep), 403)
	must(t, f.requester.delete(dep), 202)
	if d = must(t, a.get(dep), 200).Body; d["status"] != "deleted" || d["name"] != "web-1" {
		t.Errorf("deleted at once: %v", d)
	}
}

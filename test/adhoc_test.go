package test

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdHocCommands(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	orig := must(t, a.get("settings/jobs/"), 200).Body["AD_HOC_COMMANDS"]
	must(t, a.patch("settings/jobs/", map[string]any{"AD_HOC_COMMANDS": []string{"command", "shell", "ping", "debug"}}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"AD_HOC_COMMANDS": orig}) })

	must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "shell"}), 400)
	// AWX's choice error, which the collection matches on the module name.
	if r := must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "Does not exist", "module_args": "x"}), 400); r.Body["module_name"].([]any)[0] != `"Does not exist" is not a valid choice.` {
		t.Errorf("unknown module: %s", r.Raw)
	}
	must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "debug", "module_args": "msg={{ lookup('pipe', 'id') }}"}), 400)
	if r := must(t, a.do("OPTIONS", "ad_hoc_commands/", nil), 200); r.Body["actions"].(map[string]any)["POST"] == nil {
		t.Fatalf("OPTIONS should allow POST: %v", r.Body)
	}

	r := must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "debug", "module_args": "var=inventory_hostname", "limit": "h1:h2"}), 201)
	if r.Body["name"] != "debug" || r.Body["status"] != "pending" || r.Body["launch_type"] != "manual" {
		t.Fatalf("created: %v", r.Body)
	}
	cmd := waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", r.id()))
	out := a.get(fmt.Sprintf("ad_hoc_commands/%d/stdout/?format=txt", cmd.id())).Raw
	if cmd.Body["status"] != "successful" {
		t.Fatalf("status %v: %v\n%s", cmd.Body["status"], cmd.Body["job_explanation"], out)
	}
	if !strings.Contains(out, `"inventory_hostname": "h1"`) || !strings.Contains(out, `"inventory_hostname": "h2"`) || strings.Contains(out, `"h3"`) {
		t.Fatalf("stdout:\n%s", out)
	}
	if !strings.HasPrefix(cmd.Body["job_args"].(string), "ansible h1:h2 -m debug") {
		t.Fatalf("job_args: %v", cmd.Body["job_args"])
	}
	ev := a.get(fmt.Sprintf("ad_hoc_commands/%d/events/?event=runner_on_ok", cmd.id()))
	if ev.count() != 2 {
		t.Fatalf("events: %v", ev.Body)
	}
	h1 := a.get(fmt.Sprintf("inventories/%d/hosts/?name=h1", f.inv)).results()[0]
	h1id := int64(h1["id"].(float64))
	if r := a.get(fmt.Sprintf("hosts/%d/ad_hoc_command_events/", h1id)); r.count() == 0 {
		t.Fatalf("host events: %v", r.Body)
	}
	if r := a.get(fmt.Sprintf("hosts/%d/ad_hoc_commands/", h1id)); r.count() != 1 {
		t.Fatalf("host commands: %v", r.Body)
	}
	if r := a.get(fmt.Sprintf("inventories/%d/ad_hoc_commands/", f.inv)); r.count() != 1 {
		t.Fatalf("inventory commands: %v", r.Body)
	}
	// Commands cannot be modified.
	must(t, a.patch(fmt.Sprintf("ad_hoc_commands/%d/", cmd.id()), map[string]any{"limit": "h3"}), 405)

	// Through a group: the limit is the group name.
	g := must(t, a.post(fmt.Sprintf("inventories/%d/groups/", f.inv), map[string]any{"name": "grp"}), 201).id()
	must(t, a.post(fmt.Sprintf("groups/%d/hosts/", g), map[string]any{"name": "h3", "inventory": f.inv}), 204)
	gc := must(t, a.post(fmt.Sprintf("groups/%d/ad_hoc_commands/", g), map[string]any{"module_name": "ping"}), 201)
	if gc.Body["limit"] != "grp" || gc.Body["inventory"] != float64(f.inv) {
		t.Fatalf("group command: %v", gc.Body)
	}
	if done := waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", gc.id())); done.Body["status"] != "successful" {
		t.Fatalf("group command: %v\n%s", done.Body, a.get(fmt.Sprintf("ad_hoc_commands/%d/stdout/?format=txt", gc.id())).Raw)
	}

	// Credentials with ASK passwords need them at launch and relaunch.
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("ask"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "me", "password": "ASK"}}), 201).id()
	r = must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping", "credential": cred}), 400)
	if !strings.Contains(r.Raw, "ssh_password") {
		t.Fatalf("passwords needed: %s", r.Raw)
	}
	r = must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping", "credential": cred, "ssh_password": "pw"}), 201)
	if strings.Contains(r.Raw, "pw\"") || strings.Contains(r.Raw, "start_args") {
		t.Fatalf("password leaked: %s", r.Raw)
	}
	waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", r.id()))
	rl := must(t, a.get(fmt.Sprintf("ad_hoc_commands/%d/relaunch/", r.id())), 200)
	if fmt.Sprint(rl.Body["passwords_needed_to_start"]) != "[ssh_password]" {
		t.Fatalf("relaunch info: %v", rl.Body)
	}
	must(t, a.post(fmt.Sprintf("ad_hoc_commands/%d/relaunch/", r.id()), nil), 400)
	re := must(t, a.post(fmt.Sprintf("ad_hoc_commands/%d/relaunch/", cmd.id()), nil), 201)
	if re.Body["module_args"] != "var=inventory_hostname" || re.Body["limit"] != "h1:h2" {
		t.Fatalf("relaunched: %v", re.Body)
	}
	waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", re.id()))

	// RBAC: inventory read is not enough; adhoc_role is.
	user, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "inventories", f.inv, "read_role")}), 204)
	must(t, user.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping"}), 403)
	if r := user.get("ad_hoc_commands/"); r.count() < 4 {
		t.Fatalf("inventory readers see commands: %d", r.count())
	}
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "inventories", f.inv, "adhoc_role")}), 204)
	uc := must(t, user.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping"}), 201)
	// The credential must be usable by the user.
	must(t, user.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping", "credential": cred, "ssh_password": "x"}), 403)
	waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", uc.id()))
	must(t, user.delete(fmt.Sprintf("ad_hoc_commands/%d/", uc.id())), 403)
	must(t, a.delete(fmt.Sprintf("ad_hoc_commands/%d/", uc.id())), 204)
}

func TestAdHocModuleChoices(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.do("OPTIONS", "ad_hoc_commands/", nil), 200)
	post := r.Body["actions"].(map[string]any)["POST"].(map[string]any)
	choices, _ := post["module_name"].(map[string]any)["choices"].([]any)
	found := false
	for _, c := range choices {
		if c.([]any)[0] == "ping" {
			found = true
		}
	}
	if !found {
		t.Fatalf("module_name choices: %v", choices)
	}
}

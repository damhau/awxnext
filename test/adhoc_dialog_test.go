package test

import (
	"fmt"
	"testing"
)

// What the ad hoc dialog sends and reads (roadmap M7-05): the credentials
// list shows ASK inputs, a command without their passwords is refused with
// passwords_needed_to_start, and one with credential_passwords and a chosen
// execution environment is created with that environment.
func TestAdHocDialogRequest(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("ask"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "me", "password": "ASK", "become_password": "ASK"}}), 201).id()
	listed := must(t, a.get(fmt.Sprintf("credentials/?id=%d", cred)), 200).results()[0]["inputs"].(map[string]any)
	if listed["password"] != "ASK" || listed["become_password"] != "ASK" {
		t.Fatalf("listed inputs: %v", listed)
	}
	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "quay.io/ansible/awx-ee:24.6.1"}), 201).id()
	url := fmt.Sprintf("inventories/%d/ad_hoc_commands/", f.inv)
	body := map[string]any{"module_name": "ping", "credential": cred, "execution_environment": ee,
		"credential_passwords": map[string]any{"ssh_password": "pw"}}
	r := must(t, a.post(url, body), 400)
	if needed := fmt.Sprint(r.Body["passwords_needed_to_start"]); needed != "[become_password]" {
		t.Fatalf("passwords needed: %v", r.Body)
	}
	body["credential_passwords"] = map[string]any{"ssh_password": "pw", "become_password": "bpw"}
	cmd := must(t, a.post(url, body), 201)
	if cmd.Body["execution_environment"] != float64(ee) {
		t.Fatalf("execution environment: %v", cmd.Body["execution_environment"])
	}
}

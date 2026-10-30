package test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/resources"
)

// The awx-manage instance commands, with AWX's options and output.
func TestInstanceManagementCommands(t *testing.T) {
	a := adminClient(t)
	ctx := context.Background()
	run := func(f func(*bytes.Buffer) error) (string, error) {
		t.Helper()
		var b bytes.Buffer
		err := f(&b)
		return b.String(), err
	}
	failed := func(err error, printed bool) bool {
		var f *resources.CommandFailed
		return errors.As(err, &f) && f.Printed == printed
	}
	host, exec := uniq("prov")+".example", uniq("exec")+".example"
	t.Cleanup(func() {
		for _, h := range []string{host, exec} {
			resources.DeprovisionInstance(ctx, app, &bytes.Buffer{}, h)
		}
	})

	// provision_instance registers an instance, installed and without
	// capacity; again, it updates it.
	out, err := run(func(b *bytes.Buffer) error { return resources.ProvisionInstance(ctx, app, b, exec, "execution", "") })
	if err != nil || out != "Successfully registered instance "+exec+"\n(changed: True)\n" {
		t.Fatalf("provision: %v %q", err, out)
	}
	i := must(t, a.get("instances/?hostname="+exec), 200).results()[0]
	if i["node_type"] != "execution" || i["node_state"] != "installed" || i["capacity"] != float64(0) || i["managed"] != true {
		t.Errorf("provisioned: %v", i)
	}
	// Execution nodes join default by policy.
	if fmt.Sprint(must(t, a.get(fmt.Sprintf("instances/%d/instance_groups/", int64(i["id"].(float64)))), 200).Raw) == "" {
		t.Error("no groups")
	}
	if _, err := run(func(b *bytes.Buffer) error {
		return resources.ProvisionInstance(ctx, app, b, host, "hop", "1b6a8f9e-2e7c-4e3e-9b3a-0f1d2c3b4a59")
	}); err != nil {
		t.Fatal(err)
	}
	// By UUID, a new hostname.
	renamed := uniq("renamed") + ".example"
	if _, err := run(func(b *bytes.Buffer) error {
		return resources.ProvisionInstance(ctx, app, b, renamed, "hop", "1b6a8f9e-2e7c-4e3e-9b3a-0f1d2c3b4a59")
	}); err != nil {
		t.Fatal(err)
	}
	if r := must(t, a.get("instances/?uuid=1b6a8f9e-2e7c-4e3e-9b3a-0f1d2c3b4a59"), 200).results(); len(r) != 1 || r[0]["hostname"] != renamed {
		t.Errorf("renamed by uuid: %v", r)
	}
	host = renamed
	if _, err := run(func(b *bytes.Buffer) error { return resources.ProvisionInstance(ctx, app, b, "", "hybrid", "") }); !failed(err, false) {
		t.Errorf("no hostname: %v", err)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.ProvisionInstance(ctx, app, b, "x", "bogus", "") }); !failed(err, false) {
		t.Errorf("bad node type: %v", err)
	}

	// register_queue creates a group with its policies and lists instances.
	q := uniq("queue")
	out, err = run(func(b *bytes.Buffer) error {
		return resources.RegisterQueue(ctx, app, b, resources.Queue{Name: q, Hostnames: []string{exec, " ", exec}, Minimum: 1, MaxForks: 20})
	})
	if err != nil || out != fmt.Sprintf("Creating instance group %s\nAdded instance %s to %s\n(changed: True)\n", q, exec, q) {
		t.Fatalf("register_queue: %v %q", err, out)
	}
	g := must(t, a.get("instance_groups/?name="+q), 200).results()[0]
	gid := int64(g["id"].(float64))
	t.Cleanup(func() { a.delete(fmt.Sprintf("instance_groups/%d/", gid)) })
	if g["policy_instance_minimum"] != float64(1) || g["max_forks"] != float64(20) || fmt.Sprint(g["policy_instance_list"]) != "["+exec+"]" {
		t.Errorf("registered group: %v", g)
	}
	// Again, unchanged; a hop node or an unknown instance cannot be listed.
	out, err = run(func(b *bytes.Buffer) error {
		return resources.RegisterQueue(ctx, app, b, resources.Queue{Name: q, Hostnames: []string{exec}, Minimum: 1})
	})
	if err != nil || out != fmt.Sprintf("Instance Group already registered %s\nAdded instance %s to %s\n", q, exec, q) {
		t.Errorf("register_queue again: %v %q", err, out)
	}
	out, err = run(func(b *bytes.Buffer) error {
		return resources.RegisterQueue(ctx, app, b, resources.Queue{Name: q, Hostnames: []string{host}, Minimum: 1})
	})
	if !failed(err, true) || !strings.HasSuffix(out, "Instance does not exist or cannot run jobs: "+host+"\n") {
		t.Errorf("hop node: %v %q", err, out)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.RegisterQueue(ctx, app, b, resources.Queue{}) }); !failed(err, false) {
		t.Errorf("no queue name: %v", err)
	}

	// list_instances, as AWX prints it.
	out, err = run(func(b *bytes.Buffer) error { return resources.ListInstances(ctx, app, b, false) })
	if err != nil || !strings.Contains(out, fmt.Sprintf("[%s capacity=0 policy>=1]\n\t%s capacity=0 node_type=execution version=?\n", q, exec)) ||
		!regexp.MustCompile(`\[default capacity=\d+ policy=100%\]\n`).MatchString(out) ||
		!regexp.MustCompile(`\ttest-node capacity=\d+ node_type=hybrid version=\S+ heartbeat="\d{4}-\d\d-\d\d \d\d:\d\d:\d\d"\n`).MatchString(out) ||
		!strings.Contains(out, "[ungrouped capacity=0]\n\t"+host+" node_type=hop\n") {
		t.Errorf("list_instances:\n%s", out)
	}
	colored, _ := run(func(b *bytes.Buffer) error { return resources.ListInstances(ctx, app, b, true) })
	if !strings.Contains(colored, "\t\033[91m"+exec+" capacity=0") || !strings.Contains(colored, "\033[0m\n") {
		t.Errorf("colors:\n%q", colored)
	}

	// remove_from_queue unlists the instance; the minimum keeps one.
	out, err = run(func(b *bytes.Buffer) error { return resources.RemoveFromQueue(ctx, app, b, q, exec) })
	if err != nil || out != "Instance removed from instance group\n" {
		t.Errorf("remove_from_queue: %v %q", err, out)
	}
	if g := must(t, a.get(fmt.Sprintf("instance_groups/%d/", gid)), 200).Body; fmt.Sprint(g["policy_instance_list"]) != "[]" || g["instances"] != float64(1) {
		t.Errorf("after remove_from_queue: %v", g)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.RemoveFromQueue(ctx, app, b, uniq("none"), exec) }); !failed(err, true) {
		t.Errorf("unknown queue: %v", err)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.RemoveFromQueue(ctx, app, b, q, "nowhere.example") }); !failed(err, true) {
		t.Errorf("unknown host: %v", err)
	}

	// unregister_queue removes the group, not its instances, nor the installer's groups.
	out, err = run(func(b *bytes.Buffer) error { return resources.UnregisterQueue(ctx, app, b, q) })
	if err != nil || out != "Instance Group Removed\n(changed: True)\n" {
		t.Errorf("unregister_queue: %v %q", err, out)
	}
	must(t, a.get(fmt.Sprintf("instance_groups/%d/", gid)), 404)
	must(t, a.get("instances/?hostname="+exec), 200)
	if out, err := run(func(b *bytes.Buffer) error { return resources.UnregisterQueue(ctx, app, b, q) }); !failed(err, true) || out != "Instance group doesn't exist\n" {
		t.Errorf("unregister_queue again: %v %q", err, out)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.UnregisterQueue(ctx, app, b, "default") }); !failed(err, true) {
		t.Errorf("unregister default: %v", err)
	}

	// deprovision_instance removes the instance.
	out, err = run(func(b *bytes.Buffer) error { return resources.DeprovisionInstance(ctx, app, b, exec) })
	if err != nil || out != fmt.Sprintf("Instance Removed\nSuccessfully deprovisioned %s\n(changed: True)\n", exec) {
		t.Errorf("deprovision: %v %q", err, out)
	}
	if must(t, a.get("instances/?hostname="+exec), 200).count() != 0 {
		t.Error("still provisioned")
	}
	if out, _ := run(func(b *bytes.Buffer) error { return resources.DeprovisionInstance(ctx, app, b, exec) }); out != "No instance found matching name "+exec+"\n" {
		t.Errorf("deprovision again: %q", out)
	}
	if _, err := run(func(b *bytes.Buffer) error { return resources.DeprovisionInstance(ctx, app, b, "") }); !failed(err, false) {
		t.Errorf("no hostname: %v", err)
	}
}

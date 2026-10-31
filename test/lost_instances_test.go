package test

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/resources"
)

// A control node that stops sending heartbeats is lost, as in AWX: marked
// unavailable without capacity, so its groups' capacity drops; ready
// again, measured, when its heartbeats resume. With auto-deprovisioning it
// is deleted, and registers again if it comes back.
func TestLostInstances(t *testing.T) {
	a := adminClient(t)
	ctx := context.Background()
	host := uniq("lost-node")
	t.Cleanup(func() { resources.DeprovisionInstance(ctx, app, io.Discard, host) })
	// A node that started, in a group of its own that no template uses.
	var id int64
	if err := app.DB.QueryRow(ctx, `INSERT INTO instances (hostname, node_type, node_state, cpu, memory, cpu_capacity, mem_capacity, capacity, managed, version, last_seen)
		VALUES ($1, 'hybrid', 'ready', 2, 8589934592, 8, 61, 61, true, 'test', now()) RETURNING id`, host).Scan(&id); err != nil {
		t.Fatal(err)
	}
	g := must(t, a.post("instance_groups/", map[string]any{"name": uniq("lost-group")}), 201).id()
	t.Cleanup(func() { a.delete(fmt.Sprintf("instance_groups/%d/", g)) })
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", g), map[string]any{"id": id}), 204)
	path := fmt.Sprintf("instances/%d/", id)
	groupCapacity := func() float64 {
		t.Helper()
		return must(t, a.get(fmt.Sprintf("instance_groups/%d/", g)), 200).Body["capacity"].(float64)
	}
	if c := groupCapacity(); c != 61 {
		t.Fatalf("group capacity before: %v", c)
	}
	silent := func() {
		t.Helper()
		if _, err := app.DB.Exec(ctx, "UPDATE instances SET last_seen = now() - interval '3 minutes' WHERE hostname = $1", host); err != nil {
			t.Fatal(err)
		}
	}

	// Silent for 3 minutes: lost.
	silent()
	resources.HandleLostInstances(ctx, app, "test-node", false)
	i := must(t, a.get(path), 200).Body
	if i["node_state"] != "unavailable" || i["capacity"] != float64(0) || i["cpu_capacity"] != float64(0) || i["mem_capacity"] != float64(0) ||
		i["errors"] != jobs.LostError {
		t.Errorf("lost: %v", i)
	}
	if c := groupCapacity(); c != 0 {
		t.Errorf("group capacity of a lost node: %v", c)
	}

	// Its heartbeats resume: ready, measured again.
	jobManager.Beat(ctx, host, false)
	i = must(t, a.get(path), 200).Body
	silent() // so that dispatch leaves it alone from now on
	if i["node_state"] != "ready" || i["errors"] != "" || i["capacity"].(float64) <= 0 || i["cpu_capacity"].(float64) <= 0 {
		t.Errorf("back: %v", i)
	}
	if c := groupCapacity(); c != i["capacity"] {
		t.Errorf("group capacity of the node back: %v, want %v", c, i["capacity"])
	}

	// A node only provisioned has not started: it is not lost.
	waiting := uniq("provisioned")
	t.Cleanup(func() { resources.DeprovisionInstance(ctx, app, io.Discard, waiting) })
	if err := resources.ProvisionInstance(ctx, app, io.Discard, waiting, "control", ""); err != nil {
		t.Fatal(err)
	}

	// Auto-deprovisioning deletes the lost node, not the provisioned one.
	resources.HandleLostInstances(ctx, app, "test-node", true)
	must(t, a.get(path), 404)
	if r := must(t, a.get("instances/?hostname="+waiting), 200).results(); len(r) != 1 || r[0]["node_state"] != "installed" {
		t.Errorf("provisioned node: %v", r)
	}
	if n := must(t, a.get(fmt.Sprintf("instance_groups/%d/instances/", g)), 200).Body["count"]; n != float64(0) {
		t.Errorf("group members after deprovisioning: %v", n)
	}

	// It comes back: registered again only with auto-deprovisioning.
	jobManager.Beat(ctx, host, false)
	if n := must(t, a.get("instances/?hostname="+host), 200).Body["count"]; n != float64(0) {
		t.Errorf("registered again without auto-deprovisioning: %v", n)
	}
	jobManager.Beat(ctx, host, true)
	r := must(t, a.get("instances/?hostname="+host), 200).results()
	silent()
	if len(r) != 1 || r[0]["node_state"] != "ready" || r[0]["capacity"].(float64) <= 0 {
		t.Fatalf("registered again: %v", r)
	}
	if cp := must(t, a.get(fmt.Sprintf("instances/%d/instance_groups/", int64(r[0]["id"].(float64)))), 200).Body["count"]; cp == float64(0) {
		t.Errorf("registered again outside the policy groups")
	}
}

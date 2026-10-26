package test

import (
	"fmt"
	"testing"
)

func TestExecutionEnvironments(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.get("execution_environments/?managed=true"), 200)
	if r.count() < 1 {
		t.Fatal("no managed default execution environment")
	}
	managed := int64(r.results()[0]["id"].(float64))
	must(t, a.patch(fmt.Sprintf("execution_environments/%d/", managed), map[string]any{"image": "evil/image"}), 403)
	must(t, a.patch(fmt.Sprintf("execution_environments/%d/", managed), map[string]any{"pull": "always"}), 200)
	must(t, a.delete(fmt.Sprintf("execution_environments/%d/", managed)), 403)

	org := newOrg(t, a)
	member, mid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", org), map[string]any{"id": mid}), 204)
	orgEE := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "registry.acme.io/ee/network:2.3", "organization": org}), 201).id()
	globalEE := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "quay.io/acme/ee:1"}), 201).id()
	otherEE := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "x", "organization": newOrg(t, a)}), 201).id()
	must(t, member.get(fmt.Sprintf("execution_environments/%d/", orgEE)), 200)
	must(t, member.get(fmt.Sprintf("execution_environments/%d/", globalEE)), 200)
	must(t, member.get(fmt.Sprintf("execution_environments/%d/", otherEE)), 403)
	must(t, member.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "x"}), 403)
	must(t, member.patch(fmt.Sprintf("execution_environments/%d/", orgEE), map[string]any{"image": "y"}), 403)
	// Registry credential kind enforced.
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": org, "credential_type": credType(t, a, "ssh")}), 201).id()
	must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "x", "credential": ssh}), 400)
	reg := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": org, "credential_type": credType(t, a, "registry"),
		"inputs": map[string]any{"host": "registry.acme.io", "username": "bot", "password": "x"}}), 201).id()
	must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "x", "credential": reg}), 201)
}

func TestInstanceGroupsAndInstances(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.get("instance_groups/?name__in=default,controlplane"), 200)
	if r.count() != 2 {
		t.Fatalf("default groups: %s", r.Raw)
	}
	def := r.results()[0]
	if def["capacity"].(float64) <= 0 || def["instances"].(float64) != 1 {
		t.Fatalf("default group capacity: %v", def)
	}
	must(t, a.delete(fmt.Sprintf("instance_groups/%d/", int64(def["id"].(float64)))), 403)

	node := must(t, a.post("instances/", map[string]any{"hostname": "exec-eu-02.acme.internal", "node_type": "execution", "listener_port": 27199}), 201).id()
	must(t, a.post("instances/", map[string]any{"hostname": "ctl.acme.internal", "node_type": "control"}), 400)
	g := must(t, a.post("instance_groups/", map[string]any{"name": uniq("dmz")}), 201).id()
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", g), map[string]any{"id": node}), 204)
	r = must(t, a.get(fmt.Sprintf("instances/%d/instance_groups/", node)), 200)
	if r.count() != 1 {
		t.Fatalf("instance groups of node: %s", r.Raw)
	}
	// Container groups need a Kubernetes credential.
	must(t, a.post("instance_groups/", map[string]any{"name": uniq("k8s"), "is_container_group": true}), 201)
	org := newOrg(t, a)
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": org, "credential_type": credType(t, a, "ssh")}), 201).id()
	must(t, a.post("instance_groups/", map[string]any{"name": uniq("k8s"), "is_container_group": true, "credential": ssh}), 400)

	// Org members see the groups assigned to their org only.
	member, mid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", org), map[string]any{"id": mid}), 204)
	must(t, member.get(fmt.Sprintf("instance_groups/%d/", g)), 403)
	must(t, a.post(fmt.Sprintf("organizations/%d/instance_groups/", org), map[string]any{"id": g}), 204)
	must(t, member.get(fmt.Sprintf("instance_groups/%d/", g)), 200)
	must(t, member.post("instance_groups/", map[string]any{"name": uniq("x")}), 403)
}

func TestLabels(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	member, mid := newUser(t, a)
	outsider, _ := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", org), map[string]any{"id": mid}), 204)
	l := must(t, member.post("labels/", map[string]any{"name": "prod-critical", "organization": org}), 201).id()
	must(t, member.post("labels/", map[string]any{"name": "prod-critical", "organization": org}), 400)
	must(t, outsider.post("labels/", map[string]any{"name": "x", "organization": org}), 403)
	must(t, outsider.get(fmt.Sprintf("labels/%d/", l)), 403)
	must(t, member.delete(fmt.Sprintf("labels/%d/", l)), 403)
	must(t, a.delete(fmt.Sprintf("labels/%d/", l)), 204)
}

// Ordered relations list in their saved order, and grants record who made them.
func TestOrderedSublistAndGrantCreator(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	zeta := must(t, a.post("instance_groups/", map[string]any{"name": "zeta-" + uniq("ig")}), 201).id()
	alpha := must(t, a.post("instance_groups/", map[string]any{"name": "alpha-" + uniq("ig")}), 201).id()
	must(t, a.post(fmt.Sprintf("organizations/%d/instance_groups/", org), map[string]any{"id": zeta}), 204)
	must(t, a.post(fmt.Sprintf("organizations/%d/instance_groups/", org), map[string]any{"id": alpha}), 204)
	r := a.get(fmt.Sprintf("organizations/%d/instance_groups/", org)).results()
	if len(r) != 2 || int64(r[0]["id"].(float64)) != zeta {
		t.Fatalf("instance groups not in saved order: %v", []any{r[0]["name"], r[1]["name"]})
	}
	if by := a.get(fmt.Sprintf("organizations/%d/instance_groups/?order_by=name", org)).results(); int64(by[0]["id"].(float64)) != alpha {
		t.Fatal("explicit order_by ignored")
	}
	_, uid := newUser(t, a)
	var rd float64
	for _, d := range a.get("role_definitions/?name=Organization+Member").results() {
		rd = d["id"].(float64)
	}
	g := must(t, a.post("role_user_assignments/", map[string]any{"role_definition": rd, "user": uid, "object_id": org}), 201)
	if g.Body["created_by"] == nil {
		t.Fatalf("grant creator missing: %s", g.Raw)
	}
}

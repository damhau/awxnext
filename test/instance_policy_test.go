package test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/resources"
)

// AWX's apply_cluster_membership_policies, as instances join and leave:
// the policy list first, then minimums, then percentages, each group
// taking the instances in the fewest groups. The expected memberships are
// AWX's results for the same steps.
func TestInstanceGroupPolicies(t *testing.T) {
	a := adminClient(t)
	ctx := context.Background()
	// Instances other tests left would change the percentages.
	for _, r := range must(t, a.get("instances/?page_size=200"), 200).results() {
		if r["managed"] == false {
			must(t, a.delete(fmt.Sprintf("instances/%d/", int64(r["id"].(float64)))), 204)
		}
	}
	groupID := func(name string) int64 {
		return int64(must(t, a.get("instance_groups/?name="+name), 200).results()[0]["id"].(float64))
	}
	controlplane, def := groupID("controlplane"), groupID("default")
	want := func(g int64, hosts ...string) {
		t.Helper()
		var got []string
		for _, r := range must(t, a.get(fmt.Sprintf("instance_groups/%d/instances/?page_size=200", g)), 200).results() {
			got = append(got, r["hostname"].(string))
		}
		sort.Strings(got)
		sort.Strings(hosts)
		if strings.Join(got, ",") != strings.Join(hosts, ",") {
			t.Errorf("group %d: got %v, want %v", g, got, hosts)
		}
	}
	var created []string
	node := func(name, kind string) (string, int64) {
		t.Helper()
		host := uniq(name)
		created = append(created, host)
		return host, must(t, a.post("instances/", map[string]any{"hostname": host, "node_type": kind}), 201).id()
	}
	group := func(body map[string]any) int64 {
		t.Helper()
		body["name"] = uniq("policy")
		g := must(t, a.post("instance_groups/", body), 201).id()
		t.Cleanup(func() { a.delete(fmt.Sprintf("instance_groups/%d/", g)) })
		return g
	}
	t.Cleanup(func() {
		for _, h := range created {
			if r := a.get("instances/?hostname=" + h).results(); len(r) == 1 {
				a.delete(fmt.Sprintf("instances/%d/", int64(r[0]["id"].(float64))))
			}
		}
	})
	tn := "test-node"
	tnID := int64(must(t, a.get("instances/?hostname="+tn), 200).results()[0]["id"].(float64))

	// Alone, the control node fills every group it can.
	p := group(map[string]any{"policy_instance_minimum": 2})
	q := group(map[string]any{"policy_instance_percentage": 50})
	want(p, tn)
	want(q, tn)
	want(controlplane, tn)
	want(def, tn)

	// An execution node joins: the minimum takes it, and the percentage
	// moves to it, the instance in fewer groups.
	e1, e1ID := node("e1", "execution")
	want(p, tn, e1)
	want(q, e1)
	want(def, tn, e1)
	want(controlplane, tn)

	e2, _ := node("e2", "execution")
	e3, e3ID := node("e3", "execution")
	want(p, tn, e1)
	want(q, e2, e3)
	want(def, tn, e1, e2, e3)

	// One leaves: the minimum takes another.
	must(t, a.delete(fmt.Sprintf("instances/%d/", e1ID)), 204)
	want(p, tn, e2)
	want(q, e2, e3)

	// managed_by_policy off: only the groups that list it.
	e2ID := int64(must(t, a.get("instances/?hostname="+e2), 200).results()[0]["id"].(float64))
	must(t, a.patch(fmt.Sprintf("instances/%d/", e2ID), map[string]any{"managed_by_policy": false}), 200)
	want(p, tn, e3)
	want(q, e3)
	want(def, tn, e3)

	// Attaching lists the instance; detaching unlists it.
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", q), map[string]any{"id": e2ID}), 204)
	r := must(t, a.get(fmt.Sprintf("instance_groups/%d/", q)), 200)
	if fmt.Sprint(r.Body["policy_instance_list"]) != "["+e2+"]" {
		t.Errorf("listed: %v", r.Body["policy_instance_list"])
	}
	want(q, e2)
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", q), map[string]any{"id": e2ID, "disassociate": true}), 204)
	want(q, e3)
	// From the instance's side too.
	must(t, a.post(fmt.Sprintf("instances/%d/instance_groups/", e2ID), map[string]any{"id": p}), 204)
	want(p, tn, e2)
	must(t, a.post(fmt.Sprintf("instances/%d/instance_groups/", e2ID), map[string]any{"id": p, "disassociate": true}), 204)
	want(p, tn, e3)
	// An instance the percentage selects comes back, as in AWX.
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", def), map[string]any{"id": e3ID, "disassociate": true}), 204)
	want(def, tn, e3)

	// The list, set directly; hop nodes join no group.
	hop, hopID := node("hop", "hop")
	l := group(map[string]any{"policy_instance_list": []string{e2, hop}})
	want(l, e2)
	want(def, tn, e3)
	msg := func(r resp, text string) {
		t.Helper()
		if r.Status != 400 || r.Body["msg"] != text {
			t.Errorf("want %q: %d %s", text, r.Status, r.Raw)
		}
	}
	msg(a.post(fmt.Sprintf("instance_groups/%d/instances/", l), map[string]any{"id": hopID}), "Cannot change instance group membership of hop node : "+hop+".")
	msg(a.post(fmt.Sprintf("instance_groups/%d/instances/", controlplane), map[string]any{"id": tnID, "disassociate": true}),
		"Cannot disassociate hybrid node test-node from controlplane.")
	msg(a.post(fmt.Sprintf("instances/%d/instance_groups/", tnID), map[string]any{"id": controlplane, "disassociate": true}),
		"Cannot disassociate hybrid instance test-node from controlplane.")

	// Control nodes join controlplane only, and only by policy.
	ctl := uniq("ctl")
	created = append(created, ctl)
	var ctlID int64
	if err := app.DB.QueryRow(ctx, "INSERT INTO instances (hostname, node_type) VALUES ($1, 'control') RETURNING id", ctl).Scan(&ctlID); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error { return resources.ApplyMembershipPolicies(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	want(controlplane, tn, ctl)
	want(def, tn, e3)
	want(p, tn, e3)
	msg(a.post(fmt.Sprintf("instance_groups/%d/instances/", p), map[string]any{"id": ctlID}), "Cannot change instance group membership of control-only node: "+ctl+".")

	// AWX's validation.
	field := func(r resp, f, text string) {
		t.Helper()
		if r.Status != 400 || !strings.Contains(r.Raw, `"`+f+`"`) || !strings.Contains(r.Raw, text) {
			t.Errorf("%s: want %q: %d %s", f, text, r.Status, r.Raw)
		}
	}
	gq := fmt.Sprintf("instance_groups/%d/", q)
	field(a.patch(gq, map[string]any{"policy_instance_percentage": 101}), "policy_instance_percentage", "Ensure this value is less than or equal to 100.")
	field(a.patch(gq, map[string]any{"policy_instance_minimum": -1}), "policy_instance_minimum", "Ensure this value is greater than or equal to 0.")
	field(a.patch(gq, map[string]any{"max_forks": -1}), "max_forks", "Ensure this value is greater than or equal to 0.")
	field(a.patch(gq, map[string]any{"max_concurrent_jobs": -1}), "max_concurrent_jobs", "Ensure this value is greater than or equal to 0.")
	field(a.patch(gq, map[string]any{"policy_instance_list": []string{e3, e3}}), "policy_instance_list", "Duplicate entry "+e3+".")
	field(a.patch(gq, map[string]any{"policy_instance_list": []string{"nowhere.example"}}), "policy_instance_list", "nowhere.example is not a valid hostname of an existing instance.")
	gd := fmt.Sprintf("instance_groups/%d/", def)
	field(a.patch(gd, map[string]any{"name": "other"}), "name", "default instance group name may not be changed.")
	field(a.patch(gd, map[string]any{"policy_instance_percentage": 50}), "policy_instance_percentage",
		"default instance group policy_instance_percentage may not be changed from the initial value set by the installer.")
	field(a.patch(gd, map[string]any{"policy_instance_list": []string{e3}}), "policy_instance_list", "default instance group policy_instance_list may not be changed.")
	field(a.patch(fmt.Sprintf("instance_groups/%d/", controlplane), map[string]any{"is_container_group": true}), "is_container_group",
		"controlplane instance group is_container_group may not be changed.")
	must(t, a.patch(gd, map[string]any{"policy_instance_percentage": 100, "max_forks": 0}), 200)
	pods := group(map[string]any{"is_container_group": true})
	field(a.patch(fmt.Sprintf("instance_groups/%d/", pods), map[string]any{"policy_instance_minimum": 1}), "policy_instance_minimum", "Containerized instances may not be managed via the API")
	if n := must(t, a.get(fmt.Sprintf("instance_groups/%d/instances/", pods)), 200).count(); n != 0 {
		t.Errorf("container group members: %d", n)
	}
}

// max_concurrent_jobs and max_forks hold on every group, not only container
// groups; a job that does not fit goes to the next group.
func TestInstanceGroupLimits(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	tn := int64(must(t, a.get("instances/?hostname=test-node"), 200).results()[0]["id"].(float64))
	def := int64(must(t, a.get("instance_groups/?name=default"), 200).results()[0]["id"].(float64))
	group := func(body map[string]any) int64 {
		t.Helper()
		body["name"] = uniq("limited")
		g := must(t, a.post("instance_groups/", body), 201).id()
		must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", g), map[string]any{"id": tn}), 204)
		t.Cleanup(func() { a.delete(fmt.Sprintf("instance_groups/%d/", g)) })
		return g
	}
	status := func(job int64, want string) resp {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			r := a.get(fmt.Sprintf("jobs/%d/", job))
			if r.Body["status"] == want {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %d: %v, want %s", job, r.Body["status"], want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// One job at a time: the second waits for the first.
	one := group(map[string]any{"max_concurrent_jobs": 1})
	slow := f.template(t, "slow.yml", map[string]any{"allow_simultaneous": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", slow), map[string]any{"id": one}), 204)
	first := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", slow), nil), 201).id()
	status(first, "running")
	second := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", slow), nil), 201).id()
	time.Sleep(3 * time.Second)
	if r := a.get(fmt.Sprintf("jobs/%d/", second)); r.Body["status"] != "pending" || r.Body["job_explanation"] != "Waiting for capacity in the job's instance groups." {
		t.Errorf("second job: %v %v", r.Body["status"], r.Body["job_explanation"])
	}
	must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", first), nil), 202)
	if r := status(second, "running"); int64(r.Body["instance_group"].(float64)) != one {
		t.Errorf("second job group: %v", r.Body["instance_group"])
	}
	must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", second), nil), 202)
	waitJob(t, a, fmt.Sprintf("jobs/%d/", first))
	waitJob(t, a, fmt.Sprintf("jobs/%d/", second))

	// A job of more forks than the group allows falls through to the next.
	narrow := group(map[string]any{"max_forks": 1})
	jt := f.template(t, "site.yml", nil)
	for _, g := range []int64{narrow, def} {
		must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", jt), map[string]any{"id": g}), 204)
	}
	job := f.launch(t, a, jt, nil)
	if job.Body["status"] != "successful" || int64(job.Body["instance_group"].(float64)) != def {
		t.Errorf("forks: %v in group %v", job.Body["status"], job.Body["instance_group"])
	}
	must(t, a.patch(fmt.Sprintf("instance_groups/%d/", narrow), map[string]any{"max_forks": 0}), 200)
	if job := f.launch(t, a, jt, nil); int64(job.Body["instance_group"].(float64)) != narrow {
		t.Errorf("no limit: group %v", job.Body["instance_group"])
	}
}

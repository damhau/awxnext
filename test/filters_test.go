package test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// List filters behave as in AWX's FieldLookupBackend: plain filters crossing
// a relation must match the same related row, chain__ filters each match on
// their own, and host_filter and role_level apply.
func TestFilterSemantics(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	group := func(name, desc string) int64 {
		return must(t, a.post(fmt.Sprintf("inventories/%d/groups/", inv), map[string]any{"name": name, "description": desc}), 201).id()
	}
	ga, gb, gc := group("A", ""), group("B", ""), group("C", "d1")
	host := func(name string, groups ...int64) {
		id := must(t, a.post("hosts/", map[string]any{"name": name, "inventory": inv}), 201).id()
		for _, g := range groups {
			must(t, a.post(fmt.Sprintf("groups/%d/hosts/", g), map[string]any{"id": id}), 204)
		}
	}
	host("h1", ga, gb)
	host("h2", ga, gc)
	host("h3")

	hosts := func(query string) string {
		t.Helper()
		return strings.Join(names(must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&%s", inv, query)), 200)), ",")
	}
	for query, want := range map[string]string{
		"groups__name=A":                                                               "h1,h2",
		"groups__name=A&groups__name=B":                                                "", // no group is named both
		"chain__groups__name=A&chain__groups__name=B":                                  "h1",
		"groups__name=A&groups__description=d1":                                        "", // group A has no description
		"chain__groups__name=A&chain__groups__description=d1":                          "h2",
		fmt.Sprintf("groups=%d&groups__description=d1", ga):                            "",
		fmt.Sprintf("chain__groups=%d&chain__groups__description=d1", ga):              "h2",
		"not__groups__name=B":                                                          "h2,h3",
		"or__groups__name=B&or__groups__name=C":                                        "h1,h2",
		fmt.Sprintf("inventory__organization=%d&inventory__name__startswith=inv", org): "h1,h2,h3",
		"host_filter=" + url.QueryEscape("groups__name=B or name=h3"):                  "h1,h3",
		"host_filter=" + url.QueryEscape("name__startswith=h and not groups__name=A"):  "h3",
	} {
		if got := hosts(query); got != want {
			t.Errorf("hosts?%s = [%s], want [%s]", query, got, want)
		}
	}
	must(t, a.get("hosts/?host_filter="+url.QueryEscape("(name=h1")), 400)

	// role_level: objects on which the user holds the role, admin implying the others.
	inv2 := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	u, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Use"), "user": uid, "object_id": inv}), 201)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Admin"), "user": uid, "object_id": inv2}), 201)
	invs := func(c *client, role string) string {
		t.Helper()
		var ids []string
		for _, r := range must(t, c.get(fmt.Sprintf("inventories/?role_level=%s&id__in=%d,%d&order_by=id", role, inv, inv2)), 200).results() {
			ids = append(ids, fmt.Sprint(r["id"]))
		}
		return strings.Join(ids, ",")
	}
	both, second := fmt.Sprintf("%d,%d", inv, inv2), fmt.Sprint(inv2)
	for role, want := range map[string]string{"read_role": both, "use_role": both, "admin_role": second, "update_role": second, "adhoc_role": second} {
		if got := invs(u, role); got != want {
			t.Errorf("inventories?role_level=%s = [%s], want [%s]", role, got, want)
		}
	}
	if got := invs(a, "admin_role"); got != both {
		t.Errorf("superuser role_level=admin_role = [%s]", got)
	}
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization NotificationTemplate Admin"), "user": uid, "object_id": org}), 201)
	if r := must(t, u.get(fmt.Sprintf("organizations/?role_level=notification_admin_role&id=%d", org)), 200); r.count() != 1 {
		t.Errorf("notification_admin_role: %s", r.Raw)
	}
	if r := must(t, u.get(fmt.Sprintf("organizations/?role_level=admin_role&id=%d", org)), 200); r.count() != 0 {
		t.Errorf("admin_role: %s", r.Raw)
	}
	must(t, a.get("jobs/?role_level=admin_role"), 400)
	must(t, a.get("inventories/?role_level=bogus_role"), 400)
}

// AWX's reserved query parameters are not field filters, and type takes a
// comma-separated list of types as AWX's TypeFilterBackend does.
func TestReservedParamsAndType(t *testing.T) {
	a := adminClient(t)
	f := newLaunchFixture(t)
	jt := f.template(t, "site.yml", nil)
	job := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 201).id()
	// awx jobs launch --monitor and awx jobs stdout list events with no_truncate.
	must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?no_truncate=True&order_by=counter", job)), 200)
	for _, q := range []string{"no_truncate=1", "limit=web", "validate=True"} {
		if n := must(t, a.get(fmt.Sprintf("jobs/?id=%d&%s", job, q)), 200).count(); n != 1 {
			t.Errorf("jobs/?%s filtered: %d", q, n)
		}
	}

	types := func(path string) map[string]int {
		out := map[string]int{}
		for _, r := range must(t, a.get(path), 200).results() {
			out[r["type"].(string)]++
		}
		return out
	}
	got := types(fmt.Sprintf("unified_jobs/?type=job,project_update&page_size=200&id__gte=%d", job-5))
	if got["job"] == 0 || got["project_update"] == 0 || len(got) != 2 {
		t.Errorf("unified_jobs/?type=job,project_update: %v", got)
	}
	if got := types(fmt.Sprintf("unified_jobs/?type=job&id=%d", job)); got["job"] != 1 || len(got) != 1 {
		t.Errorf("unified_jobs/?type=job: %v", got)
	}
	if n := must(t, a.get(fmt.Sprintf("organizations/?type=organization&id=%d", f.org)), 200).count(); n != 1 {
		t.Errorf("organizations/?type=organization: %d", n)
	}
	if n := must(t, a.get(fmt.Sprintf("organizations/?type=team&id=%d", f.org)), 200).count(); n != 0 {
		t.Errorf("organizations/?type=team: %d", n)
	}
}

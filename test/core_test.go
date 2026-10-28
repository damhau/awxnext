package test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRootAndPing(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.get("/api/"), 200)
	if r.Body["current_version"] != "/api/v2/" {
		t.Fatalf("bad root: %v", r.Body)
	}
	r = must(t, (&client{t: t}).get("ping/"), 200)
	if r.Body["version"] != "24.6.1" {
		t.Fatalf("bad ping: %v", r.Body)
	}
	r = must(t, a.get("/api/v2/"), 200)
	for _, k := range []string{"organizations", "users", "teams", "ping", "me", "settings"} {
		if _, ok := r.Body[k]; !ok {
			t.Errorf("api root missing %s", k)
		}
	}
	must(t, (&client{t: t}).get("organizations/"), 401)
}

func TestProductHeaders(t *testing.T) {
	setup(t)
	res, err := http.Get(srv.URL + "/api/v2/ping/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("X-API-Product-Name") != "AWX" || res.Header.Get("X-API-Product-Version") == "" {
		t.Fatalf("missing product headers: %v", res.Header)
	}
}

func TestOrganizationCRUD(t *testing.T) {
	a := adminClient(t)
	name := uniq("org")
	r := must(t, a.post("organizations/", map[string]any{"name": name, "description": "d"}), 201)
	id := r.id()
	if r.Body["url"] != fmt.Sprintf("/api/v2/organizations/%d/", id) {
		t.Fatalf("bad url %v", r.Body["url"])
	}
	sf := r.Body["summary_fields"].(map[string]any)
	if _, ok := sf["object_roles"].(map[string]any)["admin_role"]; !ok {
		t.Fatalf("missing object roles: %v", sf)
	}
	r = must(t, a.post("organizations/", map[string]any{"name": name}), 400)
	if _, ok := r.Body["name"]; !ok {
		t.Fatalf("expected name error: %s", r.Raw)
	}
	must(t, a.post("organizations/", map[string]any{}), 400)
	r = must(t, a.patch(fmt.Sprintf("organizations/%d/", id), map[string]any{"description": "changed", "max_hosts": 5}), 200)
	if r.Body["description"] != "changed" || r.Body["max_hosts"].(float64) != 5 {
		t.Fatalf("patch failed: %s", r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("organizations/%d/", id), map[string]any{"max_hosts": "abc"}), 400)
	// Named URL.
	r = must(t, a.get("organizations/"+name+"/"), 200)
	if r.id() != id {
		t.Fatalf("named url resolved to %d", r.id())
	}
	// Activity stream records create and update.
	r = must(t, a.get(fmt.Sprintf("organizations/%d/activity_stream/", id)), 200)
	if r.count() != 2 {
		t.Fatalf("expected 2 activity entries, got %d: %s", r.count(), r.Raw)
	}
	must(t, a.delete(fmt.Sprintf("organizations/%d/", id)), 204)
	must(t, a.get(fmt.Sprintf("organizations/%d/", id)), 404)
}

func TestFiltering(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	prefix := uniq("flt")
	for _, n := range []string{"alpha", "beta", "gamma"} {
		must(t, a.post("teams/", map[string]any{"name": prefix + "-" + n, "organization": org, "description": "team " + n}), 201)
	}
	cases := []struct {
		q    string
		want int
	}{
		{"name__startswith=" + prefix, 3},
		{"name=" + prefix + "-beta", 1},
		{"name__icontains=" + strings.ToUpper(prefix) + "-AL", 1},
		{"name__startswith=" + prefix + "&not__name=" + prefix + "-beta", 2},
		{"or__name=" + prefix + "-alpha&or__name=" + prefix + "-gamma", 2},
		{"name__in=" + prefix + "-alpha," + prefix + "-beta", 2},
		{fmt.Sprintf("organization=%d", org), 3},
		{fmt.Sprintf("organization__id=%d&name__endswith=a", org), 3},
		{"search=team%20gam&organization=" + fmt.Sprint(org), 1},
		{fmt.Sprintf("organization=%d&description__isnull=false", org), 3},
	}
	for _, tc := range cases {
		r := must(t, a.get("teams/?"+tc.q), 200)
		if r.count() != tc.want {
			t.Errorf("%s: want %d got %d", tc.q, tc.want, r.count())
		}
	}
	r := must(t, a.get(fmt.Sprintf("teams/?organization=%d&order_by=-name", org)), 200)
	if r.results()[0]["name"] != prefix+"-gamma" {
		t.Errorf("order_by -name: got %v", r.results()[0]["name"])
	}
	r = must(t, a.get(fmt.Sprintf("teams/?organization=%d&page_size=2", org)), 200)
	if len(r.results()) != 2 || r.Body["next"] == nil || r.Body["previous"] != nil {
		t.Errorf("pagination: %s", r.Raw)
	}
	r = must(t, a.get(fmt.Sprintf("teams/?organization=%d&page_size=2&page=2", org)), 200)
	if len(r.results()) != 1 || r.Body["next"] != nil {
		t.Errorf("page 2: %s", r.Raw)
	}
	must(t, a.get(fmt.Sprintf("teams/?organization=%d&page=5", org)), 404)
	must(t, a.get("teams/?nonexistent_field=1"), 400)
	must(t, a.get("teams/?order_by=password"), 400)
}

func TestOptionsMetadata(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.do("OPTIONS", "teams/", nil), 200)
	actions := r.Body["actions"].(map[string]any)
	post := actions["POST"].(map[string]any)
	org := post["organization"].(map[string]any)
	if org["required"] != true || org["type"] != "id" {
		t.Fatalf("bad organization metadata: %v", org)
	}
	if _, ok := actions["GET"].(map[string]any)["summary_fields"]; !ok {
		t.Fatal("GET metadata missing summary_fields")
	}
}

func TestRBACVisibilityAndRoles(t *testing.T) {
	a := adminClient(t)
	bob, bobID := newUser(t, a)
	org := newOrg(t, a)
	other := newOrg(t, a)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": org}), 201).id()

	// Not a member: sees no orgs or teams.
	if n := must(t, bob.get(fmt.Sprintf("organizations/?id__in=%d,%d", org, other)), 200).count(); n != 0 {
		t.Fatalf("bob should see 0 orgs, saw %d", n)
	}
	must(t, bob.get(fmt.Sprintf("organizations/%d/", org)), 403)

	// Membership grants view of the org and its teams, not the other org.
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", org), map[string]any{"id": bobID}), 204)
	if n := must(t, bob.get(fmt.Sprintf("organizations/?id__in=%d,%d", org, other)), 200).count(); n != 1 {
		t.Fatalf("bob should see 1 org, saw %d", n)
	}
	if n := must(t, bob.get(fmt.Sprintf("teams/?organization=%d", org)), 200).count(); n != 1 {
		t.Fatalf("bob should see team, saw %d", n)
	}
	r := must(t, bob.get(fmt.Sprintf("organizations/%d/", org)), 200)
	caps := r.Body["summary_fields"].(map[string]any)["user_capabilities"].(map[string]any)
	if caps["edit"] != false {
		t.Fatalf("member should not edit org: %v", caps)
	}
	must(t, bob.patch(fmt.Sprintf("organizations/%d/", org), map[string]any{"description": "x"}), 403)
	must(t, bob.post("teams/", map[string]any{"name": uniq("t"), "organization": org}), 403)

	// Grant team admin through the legacy role API.
	teamObj := must(t, a.get(fmt.Sprintf("teams/%d/", team)), 200)
	adminRole := teamObj.Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)["admin_role"].(map[string]any)
	roleID := int64(adminRole["id"].(float64))
	must(t, bob.post(fmt.Sprintf("users/%d/roles/", bobID), map[string]any{"id": roleID}), 403)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", bobID), map[string]any{"id": roleID}), 204)
	must(t, bob.patch(fmt.Sprintf("teams/%d/", team), map[string]any{"description": "bob"}), 200)
	r = must(t, a.get(fmt.Sprintf("roles/%d/users/", roleID)), 200)
	if r.count() != 1 {
		t.Fatalf("role users: %s", r.Raw)
	}
	r = must(t, bob.get(fmt.Sprintf("users/%d/roles/?order_by=id", bobID)), 200)
	names := map[string]bool{}
	for _, x := range r.results() {
		names[x["summary_fields"].(map[string]any)["resource_type"].(string)+":"+x["name"].(string)] = true
	}
	if r.count() != 2 || !names["team:Admin"] || !names["organization:Member"] {
		t.Fatalf("bob roles: %s", r.Raw)
	}
	// Org admin via role assignment API (DAB style).
	var rdID int64
	app.DB.QueryRow(t.Context(), "SELECT id FROM role_definitions WHERE name = 'Organization Admin'").Scan(&rdID)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": rdID, "user": bobID, "object_id": org}), 201)
	must(t, bob.patch(fmt.Sprintf("organizations/%d/", org), map[string]any{"description": "bob is admin"}), 200)
	must(t, bob.post("teams/", map[string]any{"name": uniq("t"), "organization": org}), 201)
	must(t, bob.post("teams/", map[string]any{"name": uniq("t"), "organization": other}), 403)

	// Disassociate removes access.
	must(t, a.post(fmt.Sprintf("users/%d/roles/", bobID), map[string]any{"id": roleID, "disassociate": true}), 204)
	r = must(t, a.get(fmt.Sprintf("roles/%d/users/", roleID)), 200)
	if r.count() != 0 {
		t.Fatalf("role users after disassociate: %s", r.Raw)
	}
}

func TestTokens(t *testing.T) {
	a := adminClient(t)
	bob, _ := newUser(t, a)
	r := must(t, bob.post("tokens/", map[string]any{"description": "ci", "scope": "read"}), 201)
	tok := r.Body["token"].(string)
	if tok == "" || tok == "************" {
		t.Fatalf("token not returned: %s", r.Raw)
	}
	tc := &client{t: t, token: tok}
	me := must(t, tc.get("me/"), 200)
	if me.results()[0]["username"] != bob.user {
		t.Fatalf("me: %s", me.Raw)
	}
	// Read-scoped token cannot write.
	must(t, tc.post("tokens/", map[string]any{}), 403)
	// Token detail masks the value afterwards.
	r = must(t, bob.get(fmt.Sprintf("tokens/%d/", r.id())), 200)
	if r.Body["token"] != "************" {
		t.Fatalf("token leaked: %v", r.Body["token"])
	}
	must(t, bob.delete(fmt.Sprintf("tokens/%d/", r.id())), 204)
	must(t, tc.get("me/"), 401)
}

func TestSessionLoginCSRF(t *testing.T) {
	setup(t)
	jar := &cookieJar{}
	c := &http.Client{Jar: jar}
	res, err := c.Post(srv.URL+"/api/login/", "application/json", strings.NewReader(`{"username":"admin","password":"admin"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("login failed: %v %v", err, res.StatusCode)
	}
	res.Body.Close()
	res, _ = c.Get(srv.URL + "/api/v2/me/")
	if res.StatusCode != 200 {
		t.Fatalf("session me: %d", res.StatusCode)
	}
	res.Body.Close()
	// Unsafe method without CSRF header is rejected.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v2/organizations/", strings.NewReader(`{"name":"`+uniq("o")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res, _ = c.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("expected CSRF 403, got %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/api/v2/organizations/", strings.NewReader(`{"name":"`+uniq("o")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRFToken", jar.get("csrftoken"))
	res, _ = c.Do(req)
	if res.StatusCode != 201 {
		t.Fatalf("expected 201 with CSRF, got %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestSettings(t *testing.T) {
	a := adminClient(t)
	bob, _ := newUser(t, a)
	must(t, bob.get("settings/jobs/"), 403)
	must(t, bob.get("settings/ui/"), 200)
	must(t, a.patch("settings/oidc/", map[string]any{"SOCIAL_AUTH_OIDC_SECRET": "s3cret", "OIDC_GROUPS_CLAIM": "roles"}), 200)
	r := must(t, a.get("settings/oidc/"), 200)
	if r.Body["SOCIAL_AUTH_OIDC_SECRET"] != "$encrypted$" || r.Body["OIDC_GROUPS_CLAIM"] != "roles" {
		t.Fatalf("settings: %s", r.Raw)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"MAX_FORKS": "lots"}), 400)
	must(t, a.delete("settings/oidc/"), 204)
	r = must(t, a.get("settings/oidc/"), 200)
	if r.Body["OIDC_GROUPS_CLAIM"] != "groups" {
		t.Fatalf("reset failed: %s", r.Raw)
	}
}

// Null for a text or choice field reads as an empty string and for a boolean
// as false, as in AWX's serializers: the awx.awx modules send null for every
// option left unset.
func TestNullInputs(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	r := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org, "kind": nil,
		"host_filter": nil, "description": nil, "prevent_instance_group_fallback": nil}), 201)
	if r.Body["kind"] != "" || r.Body["description"] != "" || r.Body["prevent_instance_group_fallback"] != false {
		t.Fatalf("null inputs: %s", r.Raw)
	}
	inv := r.id()
	must(t, a.patch(fmt.Sprintf("inventories/%d/", inv), map[string]any{"description": "d"}), 200)
	if r = must(t, a.patch(fmt.Sprintf("inventories/%d/", inv), map[string]any{"description": nil}), 200); r.Body["description"] != "" {
		t.Fatalf("null description: %s", r.Raw)
	}
	// kind cannot change after creation, and null for an empty kind is no change.
	must(t, a.patch(fmt.Sprintf("inventories/%d/", inv), map[string]any{"kind": nil, "host_filter": nil}), 200)
	must(t, a.patch(fmt.Sprintf("inventories/%d/", inv), map[string]any{"kind": "smart"}), 400)
	// Then validated as empty: a required name or a choice without "" fails.
	if r = must(t, a.post("organizations/", map[string]any{"name": nil}), 400); !strings.Contains(r.Raw, "may not be blank") {
		t.Fatalf("null name: %s", r.Raw)
	}
	r = must(t, a.post("job_templates/", map[string]any{"name": uniq("jt"), "job_type": nil}), 400)
	if !strings.Contains(r.Raw, `\"\" is not a valid choice`) {
		t.Fatalf("null job_type: %s", r.Raw)
	}
}

// Descending order puts empty values first, like AWX (PostgreSQL's default):
// in a "latest start first" job list, jobs not started yet come before the
// rest, and a job comes above the project sync it waited for.
func TestOrderingDescendingNullsFirst(t *testing.T) {
	a := adminClient(t)
	ctx := context.Background()
	var ids []int64
	insert := func(status string, started any) int64 {
		var id int64
		if err := app.DB.QueryRow(ctx, `INSERT INTO unified_jobs (type, name, status, started)
			VALUES ('project_update', 'ordering test', $1, $2) RETURNING id`, status, started).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	t.Cleanup(func() { app.DB.Exec(context.Background(), "DELETE FROM unified_jobs WHERE id = ANY($1)", ids) })
	now := time.Now()
	job := insert("successful", now)                    // created first, started last
	sync := insert("successful", now.Add(-time.Minute)) // created after it, ran before it
	queued := insert("canceled", nil)                   // never started
	r := must(t, a.get(fmt.Sprintf("unified_jobs/?id__in=%d,%d,%d&order_by=-started,-id", job, sync, queued)), 200)
	var got []int64
	for _, j := range r.results() {
		got = append(got, int64(j["id"].(float64)))
	}
	if want := []int64{queued, job, sync}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// The unified template list reports each template's own capabilities, so a
// list can offer edit, copy and delete only where they work.
func TestUnifiedTemplateCapabilities(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": org}), 201).id()
	caps := func(c *client) map[string]any {
		t.Helper()
		r := must(t, c.get(fmt.Sprintf("unified_job_templates/?id=%d", wf)), 200).results()
		if len(r) != 1 {
			t.Fatalf("template not listed: %v", r)
		}
		return r[0]["summary_fields"].(map[string]any)["user_capabilities"].(map[string]any)
	}
	if c := caps(a); c["edit"] != true || c["copy"] != true || c["delete"] != true || c["start"] != true {
		t.Fatalf("admin capabilities: %v", c)
	}
	u, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "workflow_job_templates", wf, "execute_role")}), 204)
	if c := caps(u); c["start"] != true || c["edit"] != false || c["copy"] != false || c["delete"] != false {
		t.Fatalf("executor capabilities: %v", c)
	}
}

func TestUserPasswordPlaceholderActiveAndSSO(t *testing.T) {
	a := adminClient(t)
	name := uniq("u")
	id := must(t, a.post("users/", map[string]any{"username": name, "password": "first-pass"}), 201).id()
	login := func(pass string) int {
		return (&client{t: t, user: name, pass: pass}).get("me/").Status
	}
	// Sending back what a read returns keeps the password.
	u := must(t, a.get(fmt.Sprintf("users/%d/", id)), 200).Body
	if u["password"] != "$encrypted$" || u["is_active"] != true || u["external_account"] != nil {
		t.Fatalf("user: %v", u)
	}
	must(t, a.patch(fmt.Sprintf("users/%d/", id), map[string]any{"password": "$encrypted$", "first_name": "Ann"}), 200)
	if st := login("first-pass"); st != 200 {
		t.Fatalf("password changed by the placeholder: %d", st)
	}
	// Deactivation shows, and blocks sign-in.
	if u := must(t, a.patch(fmt.Sprintf("users/%d/", id), map[string]any{"is_active": false}), 200).Body; u["is_active"] != false {
		t.Fatalf("is_active: %v", u["is_active"])
	}
	if st := login("first-pass"); st != 401 {
		t.Fatalf("inactive user signed in: %d", st)
	}
	// Users from the identity provider are marked external.
	if _, err := app.DB.Exec(context.Background(), "UPDATE users SET external_source = 'oidc', external_id = 'sub-1' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if u := must(t, a.get(fmt.Sprintf("users/%d/", id)), 200).Body; u["external_account"] != "social" || fmt.Sprint(u["auth"]) != "[map[provider:oidc]]" {
		t.Fatalf("external user: %v %v", u["external_account"], u["auth"])
	}
}

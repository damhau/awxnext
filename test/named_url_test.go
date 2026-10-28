package test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// AWX's named URLs: every resource in its graph resolves the named_url it
// reports, in AWX's formats.
func TestNamedURLs(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	formats := must(t, a.get("settings/named-url/"), 200).Body["NAMED_URL_FORMATS"].(map[string]any)
	for res, want := range map[string]string{
		"credentials":                 "<name>++<credential_type.name>+<credential_type.kind>++<organization.name>",
		"hosts":                       "<name>++<inventory.name>++<organization.name>",
		"credential_types":            "<name>+<kind>",
		"teams":                       "<name>++<organization.name>",
		"users":                       "<username>",
		"workflow_job_template_nodes": "<identifier>++<workflow_job_template.name>++<organization.name>",
	} {
		if formats[res] != want {
			t.Errorf("format of %s: %v", res, formats[res])
		}
	}

	ssh := credType(t, a, "ssh")
	org := must(t, a.get(fmt.Sprintf("organizations/%d/", f.org)), 200).Body["name"].(string)
	team := must(t, a.post("teams/", map[string]any{"name": "a+b/c", "organization": f.org}), 201).id()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("cred"), "organization": f.org, "credential_type": ssh}), 201).id()
	orphan := must(t, a.post("credentials/", map[string]any{"name": uniq("cred"), "user": 1, "credential_type": ssh}), 201).id()
	host := must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&name=h1", f.inv)), 200).results()[0]["id"]
	group := must(t, a.post(fmt.Sprintf("inventories/%d/groups/", f.inv), map[string]any{"name": uniq("g")}), 201).id()
	jt := f.template(t, "site.yml", nil)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	node := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt}), 201).id()
	label := must(t, a.post("labels/", map[string]any{"name": uniq("l"), "organization": f.org}), 201).id()
	for path, want := range map[string]string{
		fmt.Sprintf("organizations/%d/", f.org):              "/api/v2/organizations/" + url.PathEscape(org) + "/",
		fmt.Sprintf("teams/%d/", team):                       "/api/v2/teams/a[+]b%2Fc++" + org + "/",
		fmt.Sprintf("credentials/%d/", cred):                 "",
		fmt.Sprintf("credentials/%d/", orphan):               "",
		fmt.Sprintf("hosts/%v/", host):                       "",
		fmt.Sprintf("groups/%d/", group):                     "",
		fmt.Sprintf("job_templates/%d/", jt):                 "",
		fmt.Sprintf("projects/%d/", f.project):               "",
		fmt.Sprintf("inventories/%d/", f.inv):                "",
		fmt.Sprintf("workflow_job_templates/%d/", wf):        "",
		fmt.Sprintf("workflow_job_template_nodes/%d/", node): "",
		fmt.Sprintf("labels/%d/", label):                     "",
		fmt.Sprintf("credential_types/%d/", ssh):             "/api/v2/credential_types/Machine+ssh/",
		"users/1/":                                           "/api/v2/users/admin/",
	} {
		d := must(t, a.get(path), 200).Body
		nu, _ := d["related"].(map[string]any)["named_url"].(string)
		if nu == "" || (want != "" && nu != want) {
			t.Errorf("%s: named_url %q, want %q", path, nu, want)
			continue
		}
		// The named URL resolves to the same object.
		if r := must(t, a.get(nu), 200); r.Body["id"] != d["id"] {
			t.Errorf("%s resolves to %v, not %v", nu, r.Body["id"], d["id"])
		}
	}
	// A credential without an organization ends with an empty segment.
	nu := must(t, a.get(fmt.Sprintf("credentials/%d/", orphan)), 200).Body["related"].(map[string]any)["named_url"].(string)
	if !strings.HasSuffix(nu, "++Machine+ssh++/") {
		t.Errorf("credential without organization: %s", nu)
	}
	must(t, a.get("teams/a[+]b%2Fc++no-such-org/"), 404)
	must(t, a.get("hosts/h1/"), 404)
}

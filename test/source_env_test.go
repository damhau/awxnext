package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const envInventoryScript = `#!/usr/bin/env python3
import json, os
print(json.dumps({
    "all": {"hosts": ["env-host"], "vars": {
        "region": os.environ.get("REGION", "unset"),
        "count": os.environ.get("COUNT", "unset"),
        "tags": os.environ.get("TAGS", "unset"),
        "home_kept": os.environ.get("HOME") != "/nowhere",
        "update_id_kept": os.environ.get("INVENTORY_UPDATE_ID") != "hijack",
    }},
    "_meta": {"hostvars": {}},
}))
`

// A project source's variables reach its inventory script as environment
// variables, as in AWX, except names already set and HOME, USER, _, TERM
// and PATH.
func TestProjectSourceVarsAsEnvironment(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{"inventory/env.py": envInventoryScript})
	os.Chmod(filepath.Join(repo, "inventory/env.py"), 0o755)
	for _, args := range [][]string{{"add", "inventory/env.py"}, {"-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-qm", "executable"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "env", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "inventory/env.py", "source_vars": "REGION: eu-west-1\nCOUNT: 3\nTAGS: [a, b]\nHOME: /nowhere\nINVENTORY_UPDATE_ID: hijack\n"}), 201).id()
	u := waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if u.Body["status"] != "successful" {
		t.Fatalf("update %v: %v\n%s", u.Body["status"], u.Body["job_explanation"], a.get(fmt.Sprintf("inventory_updates/%v/stdout/?format=txt", u.Body["id"])).Raw)
	}
	v := a.get(fmt.Sprintf("inventories/%d/variable_data/", inv)).Body
	if v["region"] != "eu-west-1" || v["count"] != "3" || v["tags"] != "['a', 'b']" || v["home_kept"] != true || v["update_id_kept"] != true {
		t.Fatalf("inventory variables: %v", v)
	}
	if env := fmt.Sprint(u.Body["job_env"]); !strings.Contains(env, "REGION:eu-west-1") {
		t.Fatalf("job_env: %s", env)
	}
}

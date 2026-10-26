package ansible

import (
	"strings"
	"testing"
)

func TestParseInventoryJSON(t *testing.T) {
	d, err := ParseInventoryJSON([]byte(`{
		"_meta": {"hostvars": {"a": {"x": 1}, "orphan": {}}},
		"all": {"children": ["ungrouped", "web"], "vars": {"site": "lab"}},
		"web": {"hosts": ["a", "b"], "children": ["edge"], "vars": {"port": 80}},
		"ungrouped": {"hosts": ["c"]},
		"legacy": ["d"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.AllVars["site"] != "lab" {
		t.Fatalf("all vars: %v", d.AllVars)
	}
	if strings.Join(d.Hosts, ",") != "d,c,a,b,orphan" {
		t.Fatalf("hosts: %v", d.Hosts)
	}
	if d.Groups["edge"] == nil || d.Groups["ungrouped"] != nil || d.Groups["all"] != nil {
		t.Fatalf("groups: %v", d.Groups)
	}
	if strings.Join(d.Groups["legacy"].Hosts, ",") != "d" {
		t.Fatalf("legacy group: %v", d.Groups["legacy"])
	}
}

func TestHostEnabled(t *testing.T) {
	hv := map[string]any{"status": "running", "power": map[string]any{"on": false}, "flag": "no"}
	cases := []struct {
		v, val   string
		en, have bool
	}{
		{"", "", false, false},
		{"status", "running", true, true},
		{"status", "stopped", false, true},
		{"power.on", "", false, true},
		{"flag", "", false, true},
		{"missing", "", false, false},
		{"status.deep", "", false, false},
	}
	for _, c := range cases {
		en, have := hostEnabled(hv, c.v, c.val)
		if en != c.en || have != c.have {
			t.Errorf("%s=%s: got %v,%v want %v,%v", c.v, c.val, en, have, c.en, c.have)
		}
	}
}

func TestPluginConfigForcesPlugin(t *testing.T) {
	b, err := pluginConfig("amazon.aws.aws_ec2", "plugin: ansible.builtin.script\nregions: [eu-west-1]\n")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "plugin: amazon.aws.aws_ec2\n") || strings.Contains(s, "script") || !strings.Contains(s, "eu-west-1") {
		t.Fatalf("config:\n%s", s)
	}
}

func TestMergeVars(t *testing.T) {
	if _, changed := mergeVars("a: 1\n", map[string]any{"a": 1}, false); changed {
		t.Fatal("identical vars reported as changed")
	}
	next, changed := mergeVars("a: 1\n", map[string]any{"b": 2}, false)
	if !changed || !strings.Contains(next, `"a"`) || !strings.Contains(next, `"b"`) {
		t.Fatalf("merge: %s", next)
	}
	next, _ = mergeVars("a: 1\n", map[string]any{"b": 2}, true)
	if strings.Contains(next, `"a"`) {
		t.Fatalf("overwrite: %s", next)
	}
}

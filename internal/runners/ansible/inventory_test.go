package ansible

import (
	"fmt"
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

func TestHostVarAlternatives(t *testing.T) {
	hv := map[string]any{"foreman": map[string]any{"enabled": true, "id": float64(42)}, "config": map[string]any{"instanceuuid": "u-1"}, "instance_id": "i-9"}
	// Alternatives, nested keys and case-insensitive values, as AWX's satellite6 and vmware defaults need.
	if en, have := hostEnabled(hv, "foreman_enabled,foreman.enabled", "True"); !en || !have {
		t.Errorf("satellite enabled: %v %v", en, have)
	}
	for names, want := range map[string]string{
		"foreman_id,foreman.id":                    "42",
		"config.instanceUuid, config.instanceuuid": "u-1",
		"instance_id":                              "i-9",
		"missing":                                  "",
		"":                                         "",
	} {
		if got := hostInstanceID(hv, names); got != want {
			t.Errorf("%q: %q, want %q", names, got, want)
		}
	}
}

func TestDropEmptyGroups(t *testing.T) {
	d := &InventoryData{Groups: map[string]*InventoryGroup{
		"all_empty": {},
		"with_vars": {Vars: map[string]any{"x": 1}},
		"parent":    {Children: []string{"all_empty", "web"}},
		"web":       {Hosts: []string{"h1"}},
	}}
	dropEmptyGroups(d)
	if _, ok := d.Groups["all_empty"]; ok || len(d.Groups) != 3 || fmt.Sprint(d.Groups["parent"].Children) != "[web]" {
		t.Fatalf("groups: %v, parent children %v", d.Groups, d.Groups["parent"].Children)
	}
}

func TestParseInventoryUnwrapsUnsafe(t *testing.T) {
	d, err := ParseInventoryJSON([]byte(`{"_meta": {"hostvars": {"h1": {"id": {"__ansible_unsafe": "i-1"}, "tags": [{"__ansible_unsafe": "a"}], "n": 2}}},
		"all": {"children": ["web"], "vars": {"site": {"__ansible_unsafe": "dc1"}}}, "web": {"hosts": ["h1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.HostVars["h1"]) != "map[id:i-1 n:2 tags:[a]]" || d.AllVars["site"] != "dc1" {
		t.Fatalf("%v %v", d.HostVars, d.AllVars)
	}
}

package credtypes

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/jinja_cases.json from the cases below (then run jinja_oracle.py)")

type jinjaCase struct {
	Name     string            `json:"name"`
	Template string            `json:"template"`
	Vars     map[string]any    `json:"vars,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
	Want     *string           `json:"want,omitempty"`
	Error    string            `json:"error,omitempty"`
}

const casesFile = "testdata/jinja_cases.json"

// Where gonja still differs from Jinja2; none matter for credential inputs,
// which are strings and booleans.
var knownDifferences = map[string]string{
	"integers": "2 ** 10 is 1024.0 (a float) in gonja",
	"none":     "None prints as an empty string in gonja",
}

// Sample inputs for the managed types' fields.
var managedSample = map[string]any{
	"username": "svc-ansible", "password": `p@ss"word'<&>`, "security_token": "FwoGZXIvYXdzE", "host": "https://cloud.example.com:5000/v3",
	"project": "infra", "project_domain_name": "Default", "domain": "corp", "region": "RegionOne", "verify_ssl": true,
	"ssh_key_data": "-----BEGIN PRIVATE KEY-----\nMIIEvQ\"IBADAN\n-----END PRIVATE KEY-----\n", "subscription": "0000-1111", "client": "app-id",
	"secret": "s3cr3t", "tenant": "tenant-id", "cloud_environment": "AzureCloud", "oauth_token": "tok", "bearer_token": "eyJhbGci",
	"configuration": "bucket = \"tfstate\"\nprefix = \"prod\"\n", "gce_credentials": `{"type": "service_account"}`,
}

// Custom types as people write them: conditionals, loops, filters, string
// methods, whitespace control, and what must fail.
var customCases = []jinjaCase{
	{Name: "if/else on a boolean", Template: "{% if verify_ssl %}--verify{% else %}--insecure{% endif %}", Vars: map[string]any{"verify_ssl": false}},
	{Name: "elif on a string", Template: "{% if env == 'prod' %}p{% elif env == 'dev' %}d{% else %}x{% endif %}", Vars: map[string]any{"env": "dev"}},
	{Name: "loop over split", Template: "{% for h in hosts.split(',') %}{{ h | trim }}{% if not loop.last %};{% endif %}{% endfor %}", Vars: map[string]any{"hosts": "a, b ,c"}},
	{Name: "loop index", Template: "{% for i in items %}{{ loop.index }}={{ i }} {% endfor %}", Vars: map[string]any{"items": []any{"x", "y"}}},
	{Name: "default on undefined", Template: "{{ region | default('eu-west-1') }}"},
	{Name: "default on empty", Template: "[{{ region | default('eu-west-1') }}]", Vars: map[string]any{"region": ""}},
	{Name: "default boolean", Template: "{{ region | default('eu-west-1', true) }}", Vars: map[string]any{"region": ""}},
	{Name: "tojson string", Template: "{{ key | tojson }}", Vars: map[string]any{"key": "line1\nline\"2 <b>&'"}},
	{Name: "tojson object", Template: "{{ cfg | tojson }}", Vars: map[string]any{"cfg": map[string]any{"b": 1.0, "a": []any{true, nil, "x"}}}},
	{Name: "tojson non-ascii and numbers", Template: "{{ v | tojson }}", Vars: map[string]any{"v": map[string]any{"name": "Zürich ☃ 😀", "n": 3.0, "f": 2.5, "ctl": "\x01\x7f", "list": []any{}, "obj": map[string]any{}}}},
	{Name: "tojson indent", Template: "{{ v | tojson(indent=2) }}", Vars: map[string]any{"v": map[string]any{"b": []any{1.0, "x"}, "a": map[string]any{"c": true}}}},
	{Name: "tojson range", Template: "{{ range(3) | list | tojson }}"},
	{Name: "booleans", Template: "verify={{ on }} {{ off }}", Vars: map[string]any{"on": true, "off": false}},
	{Name: "integers", Template: "{{ port }} {{ port + 1 }} {{ port // 2 }} {{ port / 2 }} {{ 7 % 3 }} {{ 2 ** 10 }}", Vars: map[string]any{"port": 8443.0}},
	{Name: "float", Template: "{{ ratio }} {{ '%.2f' | format(ratio) }}", Vars: map[string]any{"ratio": 0.5}},
	{Name: "none", Template: "{{ nothing }} {{ none }} {{ true }}", Vars: map[string]any{"nothing": nil}},
	{Name: "list", Template: "{{ items }} {{ items | join(',') }} {{ items | first }} {{ items | last }} {{ items | length }}", Vars: map[string]any{"items": []any{"a", "b"}}},
	{Name: "replace and upper", Template: "{{ name | replace('-', '_') | upper }}", Vars: map[string]any{"name": "my-app"}},
	{Name: "format", Template: "{{ '%s:%s' | format(user, pw) }}", Vars: map[string]any{"user": "u", "pw": "p"}},
	{Name: "indent", Template: "cert: |\n  {{ cert | indent(2) }}", Vars: map[string]any{"cert": "line1\nline2\nline3"}},
	{Name: "whitespace control", Template: "a\n{%- if x %}\n  b\n{%- endif %}\nc", Vars: map[string]any{"x": true}},
	{Name: "trailing newline", Template: "{{ x }}\n", Vars: map[string]any{"x": "v"}},
	{Name: "set and concatenate", Template: "{% set url = host ~ ':' ~ port %}{{ url }}", Vars: map[string]any{"host": "h", "port": 22.0}},
	{Name: "ternary", Template: "{{ 'https' if tls else 'http' }}://{{ host }}", Vars: map[string]any{"tls": true, "host": "h"}},
	{Name: "in", Template: "{{ 'prod' in env }} {{ 'x' not in env }}", Vars: map[string]any{"env": "prod-eu"}},
	{Name: "is defined", Template: "{% if token is defined %}set{% else %}unset{% endif %}"},
	{Name: "raw", Template: "{% raw %}{{ not_rendered }}{% endraw %}"},
	{Name: "comment", Template: "a{# hidden #}b"},
	{Name: "range", Template: "{{ range(3) | list }} {% for i in range(1, 7, 2) %}{{ i }}{% endfor %}"},
	{Name: "dict", Template: "{{ dict(a=1)['a'] }}"},
	{Name: "string methods", Template: "{{ host.startswith('https') }} {{ host.rstrip('/') }} {{ host.upper() }}", Vars: map[string]any{"host": "https://x/"}},
	{Name: "map", Template: "{{ items | map('upper') | join(' ') }}", Vars: map[string]any{"items": []any{"a", "b"}}},
	{Name: "comparison", Template: "{{ secret | length > 0 }} {{ not secret }} {{ a and b }} {{ a or b }}", Vars: map[string]any{"secret": "s", "a": "x", "b": ""}},
	{Name: "title and capitalize", Template: "{{ s | title }} {{ s | capitalize }}", Vars: map[string]any{"s": "hello world"}},
	{Name: "sort and unique", Template: "{{ items | sort | join }} {{ items | unique | list | length }}", Vars: map[string]any{"items": []any{"b", "a", "b"}}},
	{Name: "attribute and item", Template: "{{ cfg.a }} {{ cfg['b'] }} [{{ cfg.missing }}]", Vars: map[string]any{"cfg": map[string]any{"a": "1", "b": "2"}}},
	{Name: "repeat", Template: "{{ 'x' * 3 }}"},
	{Name: "unnamed file", Template: "{{ tower.filename }} {{ awx.filename }}", Files: map[string]string{"": "/runner/env/awx_cred_1"}},
	{Name: "named file", Template: "{{ awx.filename.cfg }}", Files: map[string]string{"cfg": "/runner/env/awx_cred_1_cfg"}},
	{Name: "kubeconfig", Template: "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: {{ host }}\n{% if ca_cert %}    certificate-authority-data: {{ ca_cert }}\n{% else %}    insecure-skip-tls-verify: true\n{% endif %}  name: c\nusers:\n- name: u\n  user:\n    token: {{ token }}\n",
		Vars: map[string]any{"host": "https://k8s:6443", "ca_cert": "", "token": "abc"}},
	{Name: "assume role", Template: "{% if role_arn %}{{ role_arn }}{% endif %}", Vars: map[string]any{"role_arn": "arn:aws:iam::1:role/x"}},
	{Name: "ansible filter is not jinja", Template: "{{ x | b64encode }}", Vars: map[string]any{"x": "a"}},
	{Name: "unknown filter", Template: "{{ x | shout }}", Vars: map[string]any{"x": "a"}},
	{Name: "unclosed block", Template: "{% if x %}a"},
	{Name: "range too big", Template: "{{ range(1000000) | length }}"},
}

func managedCases() []jinjaCase {
	var out []jinjaCase
	for _, t := range Managed {
		if t.Injectors == nil {
			continue
		}
		vars := map[string]any{}
		fields, _ := t.Inputs["fields"].([]field)
		for _, f := range fields {
			id, _ := f["id"].(string)
			if v, ok := managedSample[id]; ok {
				vars[id] = v
			}
		}
		files := map[string]string{}
		if fm, ok := t.Injectors["file"].(map[string]any); ok {
			for k := range fm {
				name := strings.TrimPrefix(strings.TrimPrefix(k, "template"), ".")
				files[name] = "/runner/env/awx_cred_1_" + name
			}
		}
		for _, section := range []string{"file", "env", "extra_vars"} {
			m, _ := t.Injectors[section].(map[string]any)
			for k, v := range m {
				if s, ok := v.(string); ok {
					out = append(out, jinjaCase{Name: fmt.Sprintf("managed %s %s.%s", t.Namespace, section, k), Template: s, Vars: vars, Files: files})
				}
			}
		}
	}
	return out
}

// Injector templates render as Jinja2 renders them in AWX. The expected
// outputs come from Jinja2 itself (testdata/jinja_oracle.py).
func TestInjectorsRenderLikeAWX(t *testing.T) {
	cases := append(managedCases(), customCases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	if *update {
		b, _ := json.MarshalIndent(cases, "", " ")
		if err := os.WriteFile(casesFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("cases written: now run testdata/jinja_oracle.py")
	}
	b, err := os.ReadFile(casesFile)
	if err != nil {
		t.Fatal(err)
	}
	var oracle []jinjaCase
	if err := json.Unmarshal(b, &oracle); err != nil {
		t.Fatal(err)
	}
	byName := map[string]jinjaCase{}
	for _, c := range oracle {
		byName[c.Name] = c
	}
	for _, c := range cases {
		o, ok := byName[c.Name]
		if !ok || o.Template != c.Template || (o.Want == nil && o.Error == "") {
			t.Errorf("%s: not in %s or changed: run with -update, then jinja_oracle.py", c.Name, casesFile)
			continue
		}
		got, err := Render(o.Template, o.Vars, o.Files)
		if why, ok := knownDifferences[c.Name]; ok {
			t.Logf("%s: known difference (%s): got %q, Jinja2 gives %q", c.Name, why, got, deref(o.Want))
			continue
		}
		switch {
		case o.Error != "" && err == nil:
			t.Errorf("%s: Jinja2 fails (%s), got %q", c.Name, o.Error, got)
		case o.Error == "" && err != nil:
			t.Errorf("%s: %v; Jinja2 gives %q", c.Name, err, *o.Want)
		case o.Error == "" && got != *o.Want:
			t.Errorf("%s:\n got  %q\n want %q", c.Name, got, *o.Want)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestRenderLimits(t *testing.T) {
	if _, err := Render(`{% include "/etc/passwd" %}`, nil, nil); err == nil {
		t.Error("include must not read files")
	}
	if _, err := Render(`{% import "/etc/passwd" as x %}`, nil, nil); err == nil {
		t.Error("import must not read files")
	}
	if _, err := Render(`{% for i in range(100000) %}{{ x }}{% endfor %}`, map[string]any{"x": strings.Repeat("y", 100)}, nil); err == nil {
		t.Error("output must be bounded")
	}
	if got, err := Render(`{% for i in range(5) %}{% if i == 1 %}{{ i }}{% endif %}{% endfor %}`, nil, nil); err != nil || got != "1" {
		t.Errorf("range: %q, %v", got, err)
	}
}

// Check is AWX's check when a type is saved: fields are defined, others are not.
func TestCheck(t *testing.T) {
	fields := []string{"host", "token"}
	for tmpl, ok := range map[string]bool{
		"{{ host }}": true,
		"{% for h in host.split(',') %}{{ h }}{% endfor %}": true,
		"{{ hots }}":                           false,
		"{{ tower.filename }}":                 false,
		"{{ token | default('x') }}":           true,
		"{% if extra is defined %}{% endif %}": true,
		"{{ host | shout }}":                   false,
		"{% if host %}":                        false,
	} {
		if err := Check(tmpl, fields, nil); (err == nil) != ok {
			t.Errorf("Check(%q) = %v, want ok=%v", tmpl, err, ok)
		}
	}
	if err := Check("{{ tower.filename.cfg }}", fields, map[string]bool{"cfg": true}); err != nil {
		t.Errorf("named file: %v", err)
	}
	if err := Check("{{ tower.filename.other }}", fields, map[string]bool{"cfg": true}); err == nil {
		t.Error("an undeclared file must fail")
	}
}

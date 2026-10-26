package credtypes

import "testing"

func TestRender(t *testing.T) {
	vars := map[string]any{"token": "abc", "region": "", "key": "line1\nline\"2", "verify": true}
	files := map[string]string{"": "/runner/env/cred", "cfg": "/runner/env/cfg"}
	cases := map[string]string{
		"{{token}}":                               "abc",
		"{{ token | upper }}":                     "ABC",
		"{{ region | default('eu') }}":            "eu",
		"{{ missing }}":                           "",
		"{{ tower.filename }}":                    "/runner/env/cred",
		"{{ awx.filename.cfg }}":                  "/runner/env/cfg",
		"{{ key | tojson }}":                      `"line1\nline\"2"`,
		"verify={{verify}}":                       "verify=True",
		"a={{token}} b={{ region|default('x') }}": "a=abc b=x",
	}
	for in, want := range cases {
		got, err := Render(in, vars, files)
		if err != nil || got != want {
			t.Errorf("Render(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Render("{{ token | shout }}", vars, files); err == nil {
		t.Error("unknown filter should fail")
	}
}

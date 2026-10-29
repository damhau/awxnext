package outputs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decls(t *testing.T, src string) []Decl {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	d, err := Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParse(t *testing.T) {
	d := decls(t, `[{"name": "regions", "type": "list", "required": true}, {"name": "replicas", "type": "integer"}]`)
	if len(d) != 2 || d[0] != (Decl{"regions", "list", true}) || d[1] != (Decl{"replicas", "integer", false}) {
		t.Fatalf("%+v", d)
	}
	for _, empty := range []any{nil, map[string]any{}, []any{}} {
		if d, err := Parse(empty); err != nil || len(d) != 0 {
			t.Errorf("%v: %v %v", empty, d, err)
		}
	}
	for src, want := range map[string]string{
		`{"a": 1}`:                         "Expected a list of outputs.",
		`["a"]`:                            "Output 1: expected an object",
		`[{"name": "1x", "type": "list"}]`: `"1x" is not a valid variable name`,
		`[{"name": "a", "type": "list"}, {"name": "a", "type": "string"}]`: `Output "a" is declared twice.`,
		`[{"name": "a", "type": "array"}]`:                                 `type must be one of string, integer, float, boolean, list, object`,
		`[{"name": "a", "type": "list", "required": "yes"}]`:               "required must be true or false",
		`[{"name": "a", "type": "list", "description": "x"}]`:              `unknown key "description"`,
	} {
		var v any
		json.Unmarshal([]byte(src), &v)
		if _, err := Parse(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}

func TestConvert(t *testing.T) {
	for _, c := range []struct {
		typ  string
		in   any
		want any
		ok   bool
	}{
		{"string", "v1", "v1", true},
		{"string", float64(2), "2", true},
		{"string", 1.5, "1.5", true},
		{"string", true, nil, false},
		{"string", []any{"a"}, nil, false},
		{"integer", float64(3), int64(3), true},
		{"integer", "3", int64(3), true},
		{"integer", " -4 ", int64(-4), true},
		{"integer", 3.5, nil, false},
		{"integer", "3.5", nil, false},
		{"integer", true, nil, false},
		{"float", float64(3), float64(3), true},
		{"float", "2.5", 2.5, true},
		{"float", "x", nil, false},
		{"boolean", true, true, true},
		{"boolean", "False", false, true},
		{"boolean", "yes", nil, false},
		{"boolean", float64(1), nil, false},
		{"list", []any{"eu"}, []any{"eu"}, true},
		{"list", "eu,us", nil, false},
		{"list", "['eu']", nil, false},
		{"object", map[string]any{"a": 1.0}, map[string]any{"a": 1.0}, true},
		{"object", []any{}, nil, false},
	} {
		got, ok := Convert(c.typ, c.in)
		if ok != c.ok || (ok && fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", c.want)) {
			t.Errorf("Convert(%s, %#v) = %#v, %v; want %#v, %v", c.typ, c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCheck(t *testing.T) {
	d := decls(t, `[{"name": "regions", "type": "list", "required": true}, {"name": "replicas", "type": "integer"}, {"name": "tag", "type": "string"}]`)
	got, err := Check(d, map[string]any{"regions": []any{"eu"}, "replicas": "3", "other": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "map[other:kept regions:[eu] replicas:3]" || got["replicas"] != int64(3) {
		t.Fatalf("%#v", got)
	}
	_, err = Check(d, map[string]any{"regions": "eu,us", "replicas": 2.5})
	want := `The playbook succeeded but its outputs do not match the template: "regions" must be a list, not a string ("eu,us"); "replicas" must be an integer, not a float (2.5).`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
	_, err = Check(d, nil)
	if err == nil || !strings.Contains(err.Error(), `"regions" is required but was not set`) {
		t.Fatal(err)
	}
	// A null value counts as not set.
	if _, err := Check(d, map[string]any{"regions": []any{}, "tag": nil}); err != nil {
		t.Fatal(err)
	}
}

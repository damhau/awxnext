package vars

import "testing"

func TestPyStr(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{"plain", "plain"},
		{true, "True"},
		{nil, "None"},
		{float64(80), "80"},
		{1.5, "1.5"},
		{[]any{"a", float64(1), false}, "['a', 1, False]"},
		{map[string]any{"b": "x", "a": []any{}}, "{'a': [], 'b': 'x'}"},
		{[]any{"it's"}, `["it's"]`},
	} {
		if got := PyStr(c.in); got != c.want {
			t.Errorf("PyStr(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

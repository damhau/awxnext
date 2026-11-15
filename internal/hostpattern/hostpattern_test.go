package hostpattern

import (
	"slices"
	"strings"
	"testing"
)

func inventory() *Inventory {
	return &Inventory{
		Hosts: []string{"web01", "web02", "web03", "db01", "db02", "lb", "solo"},
		Groups: map[string][]string{
			"web":   {"web01", "web02", "web03"},
			"db":    {"db01", "db02"},
			"front": {"lb"},
			"prod":  {},
			"stale": {"gone"},
		},
		Children: map[string][]string{"prod": {"web", "db"}, "web": {"front"}},
	}
}

func names(s Set) string {
	var out []string
	for h := range s {
		out = append(out, h)
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func TestResolve(t *testing.T) {
	inv := inventory()
	for _, tc := range []struct {
		pattern, want string
		approx        bool
	}{
		{"", "db01 db02 lb solo web01 web02 web03 ", false},
		{"all", "db01 db02 lb solo web01 web02 web03", false},
		{"*", "db01 db02 lb solo web01 web02 web03", false},
		{"web01", "web01", false},
		{"web", "lb web01 web02 web03", false},
		{"prod", "db01 db02 lb web01 web02 web03", false},
		{"ungrouped", "solo", false},
		{"web01,db01", "db01 web01", false},
		{"web01:db01", "db01 web01", false},
		{"web0*", "web01 web02 web03", false},
		{"web0[12x]", "web01 web02", false},
		{"web0[12]", "", true},
		{"web0[!12]", "web03", false},
		{"db0?", "db01 db02", false},
		{"~web0[13]", "web01 web03", false},
		{"~eb", "", false},
		{"prod:&web", "lb web01 web02 web03", false},
		{"prod:!web", "db01 db02", false},
		{"!db", "lb solo web01 web02 web03", false},
		{"&web:!web02", "lb web01 web03", false},
		{"web[0:1]", "lb web01 web02 web03", true},
		{"prod:!web[0]", "db01 db02 lb web01 web02 web03", true},
		{"@limit.txt", "db01 db02 lb solo web01 web02 web03", true},
		{"~(", "db01 db02 lb solo web01 web02 web03", true},
		{"nothere", "", false},
		{"gone", "", false},
		{" web01 , , db01 ", "db01 web01", false},
	} {
		got, approx := Resolve(inv, tc.pattern)
		if names(got) != strings.TrimSpace(tc.want) || approx != tc.approx {
			t.Errorf("%q: %q approx %v, want %q approx %v", tc.pattern, names(got), approx, strings.TrimSpace(tc.want), tc.approx)
		}
	}
}

func TestSplit(t *testing.T) {
	if got := Split("web[01:03]"); len(got) != 1 {
		t.Fatalf("a range is one term: %v", got)
	}
	if got := Split("a:b,c"); strings.Join(got, "|") != "a:b|c" {
		t.Fatalf("commas first: %v", got)
	}
}

func TestGroupHostsCycle(t *testing.T) {
	inv := &Inventory{Hosts: []string{"a", "b"}, Groups: map[string][]string{"x": {"a"}, "y": {"b"}},
		Children: map[string][]string{"x": {"y"}, "y": {"x"}}}
	if got := names(inv.GroupHosts("x")); got != "a b" {
		t.Fatal(got)
	}
}

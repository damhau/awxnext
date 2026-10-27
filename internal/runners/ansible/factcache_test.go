package ansible

import (
	"reflect"
	"testing"
)

func TestParseFactCache(t *testing.T) {
	// As written by ansible-core 2.19 for set_fact with cacheable: true.
	tagged := `{"__payload__": "{\"app_color\": {\"value\": \"blue\", \"tags\": [{\"path\": \"/p.yml\", \"line_num\": 5, \"col_num\": 20, \"__ansible_type\": \"Origin\"}, {\"__ansible_type\": \"TrustedAsTemplate\"}], \"__ansible_type\": \"_AnsibleTaggedStr\"}, \"nested\": {\"value\": {\"a\": {\"value\": [{\"value\": 1, \"tags\": [], \"__ansible_type\": \"_AnsibleTaggedInt\"}, true, null], \"tags\": [], \"__ansible_type\": \"_AnsibleTaggedList\"}}, \"tags\": [], \"__ansible_type\": \"_AnsibleTaggedDict\"}, \"ansible_hostname\": \"h3\"}"}`
	want := map[string]any{"app_color": "blue", "nested": map[string]any{"a": []any{1.0, true, nil}}, "ansible_hostname": "h3"}
	for _, tc := range []struct {
		file, body, host string
		facts            map[string]any
	}{
		{"s1_h3", tagged, "h3", want},
		{"s1_h3", `{"__payload__": "{\"app_color\": \"green\"}"}`, "h3", map[string]any{"app_color": "green"}},
		{"h3", `{"app_color": "red"}`, "h3", map[string]any{"app_color": "red"}},
		// A host literally named s1_web cached by an older ansible-core.
		{"s1_web", `{"app_color": "red"}`, "s1_web", map[string]any{"app_color": "red"}},
	} {
		host, facts, ok := parseFactCache(tc.file, []byte(tc.body))
		if !ok || host != tc.host || !reflect.DeepEqual(facts, tc.facts) {
			t.Errorf("%s: got %q %v %v, want %q %v", tc.file, host, facts, ok, tc.host, tc.facts)
		}
	}
	if _, _, ok := parseFactCache("h3", []byte("not json")); ok {
		t.Error("invalid JSON accepted")
	}
}

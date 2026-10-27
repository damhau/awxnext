package resources

import (
	"reflect"
	"testing"
)

func TestWorkflowExpressions(t *testing.T) {
	env := map[string]any{
		"deploy_env": "prod", "replicas": 3.0, "regions": []any{"eu", "us"}, "empty": []any{},
		"nodes": map[string]any{"build": map[string]any{"status": "successful", "artifacts": map[string]any{"tag": "v1"}}},
	}
	for src, want := range map[string]bool{
		"deploy_env == 'prod' and replicas > 2":      true,
		"'eu' in regions":                            true,
		"not ('ap' in regions)":                      true,
		"nodes.build.artifacts.tag == 'v1'":          true,
		"(nodes['smoke']?.status ?? 'none') == 'ok'": false,
		"undefined_var":                              false,
		"empty":                                      false,
		"regions":                                    true,
		"replicas":                                   true,
		"''":                                         false,
	} {
		got, err := evalWhen(src, env)
		if err != nil || got != want {
			t.Errorf("when %q = %v, %v; want %v", src, got, err, want)
		}
	}
	if _, err := evalWhen("nodes.nope.status", env); err == nil {
		t.Error("member access on a missing node should fail")
	}
	if items, err := evalLoop("filter(regions, # != 'eu')", env); err != nil || !reflect.DeepEqual(items, []any{"us"}) {
		t.Errorf("loop: %v %v", items, err)
	}
	for _, bad := range []string{"deploy_env", "undefined_var", "replicas"} {
		if _, err := evalLoop(bad, env); err == nil {
			t.Errorf("loop over %q should fail", bad)
		}
	}
	if checkExpr("deploy_env ==") == nil {
		t.Error("syntax error not reported")
	}
}

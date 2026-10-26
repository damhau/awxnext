package tmpl

import "testing"

func TestRender(t *testing.T) {
	ctx := map[string]any{
		"job": map[string]any{"id": float64(42), "name": "deploy", "status": "failed",
			"summary_fields": map[string]any{"inventory": map[string]any{"name": "prod"}}, "tags": []any{"a", "b"}},
		"job_friendly_name": "Job",
		"url":               "https://awx/jobs/42",
	}
	cases := map[string]string{
		"{{ job_friendly_name }} #{{ job.id }} '{{ job.name }}' {{ job.status }}: {{ url }}":               "Job #42 'deploy' failed: https://awx/jobs/42",
		"{{ job.summary_fields.inventory.name | upper }}":                                                  "PROD",
		"{{ job['name'] }}/{{ job.tags.1 }}":                                                               "deploy/b",
		"{{ job.missing | default('n/a') }}":                                                               "n/a",
		`{"id": {{ job.id }}, "name": {{ job.name | tojson }}}`:                                            `{"id": 42, "name": "deploy"}`,
		"{% if job.status == 'failed' %}FAIL{% elif job.status == 'successful' %}OK{% else %}?{% endif %}": "FAIL",
		"{% if not job.missing %}none{% endif %}":                                                          "none",
		"{{ job.tags | length }}":                                                                          "2",
	}
	for src, want := range cases {
		got, err := Render(src, ctx)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", src, got, err, want)
		}
	}
	for _, bad := range []string{"{{ }}", "{% if x %}", "{% endif %}", "{{ x | nope }}", "{{ x.__class__() }}", "{% for x in y %}{% endfor %}"} {
		if _, err := Render(bad, ctx); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

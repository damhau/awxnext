package test

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestMetricsEndpoints(t *testing.T) {
	a := adminClient(t)
	a.get("organizations/")
	r := must(t, a.get("metrics/"), 200)
	for _, want := range []string{"# TYPE awx_organizations_total gauge", "awx_system_info{", `awx_instance_capacity{hostname="test-node"`, "awx_instance_remaining_capacity{"} {
		if !strings.Contains(r.Raw, want) {
			t.Fatalf("missing %q in /api/v2/metrics/:\n%.2000s", want, r.Raw)
		}
	}
	user, _ := newUser(t, a)
	must(t, user.get("metrics/"), 403)

	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	out := string(b)
	for _, want := range []string{
		`awxnext_http_requests_total{method="GET",route="/api/v2/organizations",code="200"}`,
		"# TYPE awxnext_http_request_duration_seconds histogram",
		"awxnext_db_pool_max_connections",
		"awxnext_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in /metrics:\n%.3000s", want, out)
		}
	}
}

// AWX's subsystem metrics: per node, fed by the event writer, the
// dispatcher and the workflow engine, filtered by node and metric.
func TestSubsystemMetrics(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	job := f.launch(t, a, f.template(t, "site.yml", nil), nil)
	mustStatus(t, f, job, "successful")
	value := func(text, line string) float64 {
		t.Helper()
		for _, l := range strings.Split(text, "\n") {
			if rest, ok := strings.CutPrefix(l, line+" "); ok {
				v, err := strconv.ParseFloat(rest, 64)
				if err != nil {
					t.Fatalf("%s: %v", l, err)
				}
				return v
			}
		}
		t.Fatalf("no %s in:\n%.3000s", line, text)
		return 0
	}
	r := must(t, a.get("metrics/"), 200).Raw
	for _, want := range []string{"# TYPE callback_receiver_events_insert_db gauge", "# TYPE callback_receiver_batch_events_insert_db histogram",
		`callback_receiver_batch_events_insert_db_bucket{le="10",node="test-node"}`, "# HELP task_manager__schedule_calls Number of calls to _schedule"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q", want)
		}
	}
	if v := value(r, `callback_receiver_events_insert_db{node="test-node"}`); v < 1 {
		t.Errorf("events stored: %v", v)
	}
	if v := value(r, `task_manager__schedule_calls{node="test-node"}`); v < 1 {
		t.Errorf("dispatcher passes: %v", v)
	}
	if v := value(r, `dispatcher_pool_scale_up_events{node="test-node"}`); v < 1 {
		t.Errorf("jobs started on the node: %v", v)
	}

	// Another node's published values are served too.
	other := uniq("node")
	app.DB.Exec(t.Context(), "INSERT INTO instances (hostname, node_type) VALUES ($1, 'execution')", other)
	app.DB.Exec(t.Context(), `INSERT INTO subsystem_metrics (node, data) VALUES ($1, '{"task_manager__schedule_calls": 42}')`, other)
	r = must(t, a.get("metrics/?subsystemonly=1&metric=task_manager__schedule_calls"), 200).Raw
	if value(r, fmt.Sprintf(`task_manager__schedule_calls{node="%s"}`, other)) != 42 || strings.Contains(r, "awx_organizations_total") ||
		strings.Contains(r, "callback_receiver") {
		t.Errorf("filtered by metric:\n%s", r)
	}
	r = must(t, a.get("metrics/?subsystemonly=1&node="+other), 200).Raw
	if strings.Contains(r, `node="test-node"`) || !strings.Contains(r, `callback_receiver_events_insert_db{node="`+other+`"} 0`) {
		t.Errorf("filtered by node:\n%.1500s", r)
	}
	if r = must(t, a.get("metrics/?dbonly=1"), 200).Raw; strings.Contains(r, "task_manager_") || !strings.Contains(r, "awx_organizations_total") {
		t.Errorf("dbonly:\n%.1500s", r)
	}
}

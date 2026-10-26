package test

import (
	"io"
	"net/http"
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

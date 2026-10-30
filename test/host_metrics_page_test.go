package test

import (
	"fmt"
	"testing"
)

// The host metrics page's query (roadmap M7-08, lib/hostMetrics.ts): a name
// search, deleted hosts left out, most recently automated first.
func TestHostMetricsPageQuery(t *testing.T) {
	a := adminClient(t)
	ctx := t.Context()
	prefix := uniq("page")
	app.DB.Exec(ctx, `INSERT INTO host_metrics (hostname, first_automation, last_automation, automated_counter) VALUES
		($1, now() - interval '5 days', now() - interval '2 days', 2), ($2, now() - interval '1 day', now(), 1)`, prefix+"-a", prefix+"-b")
	app.DB.Exec(ctx, `INSERT INTO host_metrics (hostname, first_automation, last_automation, automated_counter, deleted, deleted_counter, last_deleted)
		VALUES ($1, now() - interval '9 days', now() - interval '8 days', 1, true, 1, now())`, prefix+"-gone")

	names := func(q string) []string {
		var out []string
		for _, m := range must(t, a.get("host_metrics/?"+q), 200).results() {
			out = append(out, fmt.Sprint(m["hostname"]))
		}
		return out
	}
	if got := names("order_by=-last_automation&deleted=false&search=" + prefix); fmt.Sprint(got) != fmt.Sprint([]string{prefix + "-b", prefix + "-a"}) {
		t.Fatalf("without deleted: %v", got)
	}
	if got := names("order_by=-last_automation&search=" + prefix); len(got) != 3 || got[2] != prefix+"-gone" {
		t.Fatalf("with deleted: %v", got)
	}
}

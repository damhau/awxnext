package test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/bench"
)

// awxbench's steps against this server, small: what the benchmark workflow
// runs against AWX and awxnext at scale.
func TestBenchmark(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	files := map[string]string{}
	for _, f := range []string{"events.yml", "hello.yml"} {
		b, err := os.ReadFile("../bench/playbooks/" + f)
		if err != nil {
			t.Fatal(err)
		}
		files["bench/playbooks/"+f] = string(b)
	}
	repo := gitRepo(t, files)
	r, err := bench.Run(context.Background(), bench.Target{URL: srv.URL, User: "admin", Password: "admin"}, bench.Options{
		Hosts: 250, ListRequests: 5, Events: 200, Launches: 6, ConcurrentLaunches: 3,
		ProjectURL: "file://" + repo, ProjectBranch: "main", Timeout: 2 * time.Minute, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	// The loop's 200 items, plus the play's own events.
	if r.Events < 200 || r.EventsPerSecond <= 0 || r.Hosts != 250 || r.HostList.P50 <= 0 || r.HostSearch.P95 <= 0 || r.JobList.Max <= 0 ||
		r.Launches != 6 || r.LaunchesPerSecond <= 0 || r.JobsPerMinute <= 0 || r.Version == "" {
		t.Fatalf("result: %+v", r)
	}
	md := bench.Markdown([]*bench.Result{r, r}, []string{"one", "two"})
	if !strings.Contains(md, "| Measure | one | two |") || !strings.Contains(md, "| Event ingestion (events/s) |") || strings.Count(md, "\n") != 9 {
		t.Errorf("table:\n%s", md)
	}
}

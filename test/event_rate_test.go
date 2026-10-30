package test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/jobs"
)

func rateJob(t *testing.T) (int64, time.Time) {
	t.Helper()
	ctx := context.Background()
	id, err := jobs.Create(ctx, app.DB, map[string]any{"type": "job", "name": uniq("noisy"), "status": "running"})
	if err != nil {
		t.Fatal(err)
	}
	var created time.Time
	app.DB.QueryRow(ctx, "SELECT created FROM unified_jobs WHERE id = $1", id).Scan(&created)
	return id, created
}

// Over JOB_EVENT_MAX_RATE, consecutive verbose events are folded into one:
// fewer rows, the same output, every other event kept, counters contiguous.
func TestEventRateFolding(t *testing.T) {
	a := adminClient(t)
	ctx := context.Background()
	for _, runnerCounters := range []bool{false, true} {
		id, created := rateJob(t)
		out := jobs.NewOutput(app.DB, id, created, nil, nil, nil)
		out.RateLimit = 50
		var want []string
		counter := 0
		emit := func(e jobs.Event) {
			if runnerCounters {
				counter++
				e.Counter = counter
			}
			out.Emit(e)
		}
		for i := 1; i <= 3000; i++ {
			line := fmt.Sprintf("line %d", i)
			emit(jobs.Event{Event: "verbose", Stdout: line})
			want = append(want, line)
			if i%500 == 0 {
				emit(jobs.Event{Event: "runner_on_ok", Stdout: fmt.Sprintf("ok %d", i), HostName: "h1"})
				want = append(want, fmt.Sprintf("ok %d", i))
			}
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		var rows, oks, minC, maxC int
		app.DB.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE event = 'runner_on_ok'), min(counter), max(counter) FROM job_events WHERE job_id = $1", id).Scan(&rows, &oks, &minC, &maxC)
		if rows >= 1000 || oks != 6 || minC != 1 || maxC != rows || out.Folded() != 3006-rows {
			t.Errorf("runner counters %v: %d rows, %d ok, counters %d..%d, %d folded", runnerCounters, rows, oks, minC, maxC, out.Folded())
		}
		r := a.get(fmt.Sprintf("jobs/%d/stdout/?format=txt", id))
		if r.Raw != strings.Join(want, "\n")+"\n" {
			t.Errorf("runner counters %v: stdout differs (%d bytes, want %d)", runnerCounters, len(r.Raw), len(strings.Join(want, "\n"))+1)
		}
	}

	// No limit: every event is stored.
	id, created := rateJob(t)
	out := jobs.NewOutput(app.DB, id, created, nil, nil, nil)
	for i := 0; i < 500; i++ {
		out.Emit(jobs.Event{Event: "verbose", Stdout: "x"})
	}
	out.Close()
	var rows int
	app.DB.QueryRow(ctx, "SELECT count(*) FROM job_events WHERE job_id = $1", id).Scan(&rows)
	if rows != 500 || out.Folded() != 0 {
		t.Errorf("no limit: %d rows, %d folded", rows, out.Folded())
	}

	// The setting is validated.
	if r := a.patch("settings/jobs/", map[string]any{"JOB_EVENT_MAX_RATE": -1}); r.Status != 400 {
		t.Errorf("negative rate: %d %s", r.Status, r.Raw)
	}
}

// A noisy job leaves another job's event latency (from emitted to stored)
// within a bound, with the default JOB_EVENT_MAX_RATE.
func TestNoisyJobEventLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark")
	}
	measure := func(limit int) (p95, worst time.Duration, noisyRows int) {
		t.Helper()
		noisyID, noisyCreated := rateJob(t)
		quietID, quietCreated := rateJob(t)
		var mu sync.Mutex
		emitted := map[int]time.Time{}
		var latencies []time.Duration
		stored := 0
		quiet := jobs.NewOutput(app.DB, quietID, quietCreated, nil, nil, func(max int) {
			mu.Lock()
			defer mu.Unlock()
			for c := stored + 1; c <= max; c++ {
				if at, ok := emitted[c]; ok {
					latencies = append(latencies, time.Since(at))
				}
			}
			stored = max
		})
		quiet.RateLimit = limit
		noisy := jobs.NewOutput(app.DB, noisyID, noisyCreated, nil, nil, nil)
		noisy.RateLimit = limit
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				line := strings.Repeat("v", 200)
				// Up to 300,000 events between them, as fast as they can.
				for n := 0; n < 75; n++ {
					select {
					case <-stop:
						return
					default:
					}
					for i := 0; i < 1000; i++ {
						noisy.Emit(jobs.Event{Event: "verbose", Stdout: line})
					}
				}
			}()
		}
		for i := 1; i <= 100; i++ {
			mu.Lock()
			emitted[i] = time.Now()
			mu.Unlock()
			quiet.Emit(jobs.Event{Event: "runner_on_ok", Stdout: fmt.Sprintf("ok %d", i), HostName: "h1"})
			time.Sleep(20 * time.Millisecond)
		}
		close(stop)
		wg.Wait()
		quiet.Close()
		noisy.Close()
		app.DB.QueryRow(context.Background(), "SELECT count(*) FROM job_events WHERE job_id = $1", noisyID).Scan(&noisyRows)
		mu.Lock()
		defer mu.Unlock()
		if len(latencies) != 100 {
			t.Fatalf("quiet job: %d of 100 events stored", len(latencies))
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		return latencies[94], latencies[99], noisyRows
	}
	p95, worst, rows := measure(1000)
	t.Logf("with JOB_EVENT_MAX_RATE 1000: quiet job p95 %v, worst %v; noisy job stored %d rows", p95, worst, rows)
	if p95 > 750*time.Millisecond || worst > 1500*time.Millisecond {
		t.Errorf("quiet job latency p95 %v, worst %v: over the bound (750ms, 1.5s)", p95, worst)
	}
	p95, worst, rows = measure(0)
	t.Logf("without a limit: quiet job p95 %v, worst %v; noisy job stored %d rows", p95, worst, rows)
}

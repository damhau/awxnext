// Package bench measures an AWX-compatible API (awxnext, or AWX itself)
// through its public API only, so both run the same steps: event ingestion
// (a job looping over many items), list latency at scale (an inventory of
// many hosts) and launch throughput (many small jobs at once).
package bench

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Target is the API under test.
type Target struct {
	URL, User, Password string
	Insecure            bool
}

// Options sizes the runs.
type Options struct {
	// Hosts in the inventory listed for latency.
	Hosts int
	// ListRequests per list measured.
	ListRequests int
	// Events the event job emits (items of one loop).
	Events int
	// Launches of the small job, ConcurrentLaunches at a time.
	Launches, ConcurrentLaunches int
	// ProjectURL and ProjectBranch hold bench/playbooks.
	ProjectURL, ProjectBranch string
	// ContainerGroup runs the jobs in a container group the run creates
	// (pods with the EE image), on AWX as on awxnext.
	ContainerGroup bool
	// Timeout bounds each wait.
	Timeout time.Duration
	Log     func(format string, args ...any)
}

// Latency is a list's response times, in milliseconds.
type Latency struct {
	P50, P95, Max float64
}

// Result is a run's measures.
type Result struct {
	Target  string `json:"target"`
	Version string `json:"version"`
	// Events stored, and the seconds from the job's start until all were
	// (event_processing_finished).
	Events          int     `json:"events"`
	EventSeconds    float64 `json:"event_seconds"`
	EventsPerSecond float64 `json:"events_per_second"`
	Hosts           int     `json:"hosts"`
	// HostList is GET /hosts/?inventory=N&page_size=200, HostSearch a
	// search in it, JobList GET /unified_jobs/?page_size=50.
	HostList   Latency `json:"host_list_ms"`
	HostSearch Latency `json:"host_search_ms"`
	JobList    Latency `json:"job_list_ms"`
	// Launches accepted per second, and finished jobs per minute.
	Launches          int     `json:"launches"`
	LaunchesPerSecond float64 `json:"launches_per_second"`
	JobsPerMinute     float64 `json:"jobs_per_minute"`
}

type client struct {
	t    Target
	http *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/api/v2/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.t.URL, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(c.t.User, c.t.Password)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

func (c *client) create(ctx context.Context, path string, body any) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	_, err := c.do(ctx, "POST", path, body, &out)
	return out.ID, err
}

// Run sets up its objects (named bench-<time>) and measures.
func Run(ctx context.Context, t Target, o Options) (*Result, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Minute
	}
	if o.ConcurrentLaunches <= 0 {
		o.ConcurrentLaunches = 10
	}
	c := &client{t: t, http: &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: t.Insecure}, MaxIdleConnsPerHost: 64}}} //nolint:gosec // Target.Insecure
	res := &Result{Target: t.URL}
	var ping struct {
		Version string `json:"version"`
	}
	if _, err := c.do(ctx, "GET", "ping/", nil, &ping); err != nil {
		return nil, err
	}
	res.Version = ping.Version
	name := fmt.Sprintf("bench-%d", time.Now().Unix())
	org, err := c.create(ctx, "organizations/", map[string]any{"name": name})
	if err != nil {
		return nil, err
	}
	local, err := c.create(ctx, "inventories/", map[string]any{"name": name + "-local", "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	if err != nil {
		return nil, err
	}
	if _, err := c.create(ctx, "hosts/", map[string]any{"name": "localhost", "inventory": local}); err != nil {
		return nil, err
	}
	project, err := c.create(ctx, "projects/", map[string]any{"name": name, "organization": org, "scm_type": "git",
		"scm_url": o.ProjectURL, "scm_branch": o.ProjectBranch})
	if err != nil {
		return nil, err
	}
	o.Log("syncing the project")
	if err := waitFor(ctx, o.Timeout, func() (bool, error) {
		var p struct{ Status string }
		if _, err := c.do(ctx, "GET", fmt.Sprintf("projects/%d/", project), nil, &p); err != nil {
			return false, err
		}
		if p.Status == "failed" || p.Status == "error" {
			return false, fmt.Errorf("project sync %s", p.Status)
		}
		return p.Status == "successful", nil
	}); err != nil {
		return nil, err
	}
	var group int64
	if o.ContainerGroup {
		if group, err = c.create(ctx, "instance_groups/", map[string]any{"name": name + "-pods", "is_container_group": true}); err != nil {
			return nil, err
		}
	}
	template := func(playbook string, extra map[string]any) (int64, error) {
		if extra == nil {
			extra = map[string]any{}
		}
		ev, _ := json.Marshal(extra)
		id, err := c.create(ctx, "job_templates/", map[string]any{"name": name + "-" + strings.TrimSuffix(playbook, ".yml"), "inventory": local,
			"project": project, "playbook": "bench/playbooks/" + playbook, "extra_vars": string(ev), "allow_simultaneous": true})
		if err != nil {
			return 0, err
		}
		if group != 0 {
			if _, err := c.do(ctx, "POST", fmt.Sprintf("job_templates/%d/instance_groups/", id), map[string]any{"id": group}, nil); err != nil {
				return 0, err
			}
		}
		return id, nil
	}

	// Event ingestion.
	events, err := template("events.yml", map[string]any{"count": o.Events})
	if err != nil {
		return nil, err
	}
	o.Log("event job: %d events", o.Events)
	job, err := c.create(ctx, fmt.Sprintf("job_templates/%d/launch/", events), map[string]any{})
	if err != nil {
		return nil, err
	}
	var started, done time.Time
	if err := waitFor(ctx, o.Timeout, func() (bool, error) {
		var j struct {
			Status                  string
			Started                 *time.Time
			EventProcessingFinished bool `json:"event_processing_finished"`
		}
		if _, err := c.do(ctx, "GET", fmt.Sprintf("jobs/%d/", job), nil, &j); err != nil {
			return false, err
		}
		if j.Started != nil {
			started = *j.Started
		}
		switch j.Status {
		case "failed", "error", "canceled":
			return false, fmt.Errorf("event job %s", j.Status)
		case "successful":
			if j.EventProcessingFinished {
				done = time.Now()
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	var count struct {
		Count int `json:"count"`
	}
	if _, err := c.do(ctx, "GET", fmt.Sprintf("jobs/%d/job_events/?page_size=1", job), nil, &count); err != nil {
		return nil, err
	}
	res.Events, res.EventSeconds = count.Count, done.Sub(started).Seconds()
	if res.EventSeconds > 0 {
		res.EventsPerSecond = float64(res.Events) / res.EventSeconds
	}

	// List latency at scale.
	big, err := c.create(ctx, "inventories/", map[string]any{"name": name + "-hosts", "organization": org})
	if err != nil {
		return nil, err
	}
	o.Log("creating %d hosts", o.Hosts)
	for i := 0; i < o.Hosts; i += 100 {
		var hosts []map[string]any
		for j := i; j < min(i+100, o.Hosts); j++ {
			hosts = append(hosts, map[string]any{"name": fmt.Sprintf("host-%06d.example.com", j)})
		}
		if _, err := c.do(ctx, "POST", "bulk/host_create/", map[string]any{"inventory": big, "hosts": hosts}, nil); err != nil {
			return nil, err
		}
	}
	res.Hosts = o.Hosts
	measure := func(path string) (Latency, error) {
		var ms []float64
		for i := 0; i < o.ListRequests; i++ {
			t0 := time.Now()
			if _, err := c.do(ctx, "GET", path, nil, nil); err != nil {
				return Latency{}, err
			}
			ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
		}
		return latency(ms), nil
	}
	o.Log("measuring lists")
	if res.HostList, err = measure(fmt.Sprintf("hosts/?inventory=%d&page_size=200&order_by=name", big)); err != nil {
		return nil, err
	}
	if res.HostSearch, err = measure(fmt.Sprintf("hosts/?inventory=%d&search=host-0042&page_size=200", big)); err != nil {
		return nil, err
	}
	if res.JobList, err = measure("unified_jobs/?page_size=50&order_by=-id"); err != nil {
		return nil, err
	}

	// Launch throughput.
	hello, err := template("hello.yml", nil)
	if err != nil {
		return nil, err
	}
	o.Log("launching %d jobs, %d at a time", o.Launches, o.ConcurrentLaunches)
	ids := make([]int64, o.Launches)
	var errs []error
	var mu sync.Mutex
	sem := make(chan struct{}, o.ConcurrentLaunches)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			id, err := c.create(ctx, fmt.Sprintf("job_templates/%d/launch/", hello), map[string]any{})
			mu.Lock()
			defer mu.Unlock()
			ids[i] = id
			if err != nil {
				errs = append(errs, err)
			}
		}(i)
	}
	wg.Wait()
	submitted := time.Since(t0)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := waitFor(ctx, o.Timeout, func() (bool, error) {
		var page struct {
			Count int `json:"count"`
		}
		if _, err := c.do(ctx, "GET", fmt.Sprintf("jobs/?job_template=%d&status__in=successful,failed,error,canceled&page_size=1", hello), nil, &page); err != nil {
			return false, err
		}
		return page.Count >= o.Launches, nil
	}); err != nil {
		return nil, err
	}
	finished := time.Since(t0)
	var failed struct {
		Count int `json:"count"`
	}
	c.do(ctx, "GET", fmt.Sprintf("jobs/?job_template=%d&status__in=failed,error,canceled&page_size=1", hello), nil, &failed)
	if failed.Count > 0 {
		return nil, fmt.Errorf("%d of the small jobs did not succeed", failed.Count)
	}
	res.Launches = o.Launches
	res.LaunchesPerSecond = float64(o.Launches) / submitted.Seconds()
	res.JobsPerMinute = float64(o.Launches) / finished.Minutes()
	return res, nil
}

func latency(ms []float64) Latency {
	if len(ms) == 0 {
		return Latency{}
	}
	sort.Float64s(ms)
	at := func(p float64) float64 { return ms[min(len(ms)-1, int(p*float64(len(ms))))] }
	return Latency{P50: at(0.5), P95: at(0.95), Max: ms[len(ms)-1]}
}

func waitFor(ctx context.Context, timeout time.Duration, done func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Markdown is results side by side, a column per target.
func Markdown(results []*Result, names []string) string {
	var b strings.Builder
	b.WriteString("| Measure |")
	for _, n := range names {
		b.WriteString(" " + n + " |")
	}
	b.WriteString("\n|---|")
	for range names {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	row := func(label string, f func(r *Result) string) {
		b.WriteString("| " + label + " |")
		for _, r := range results {
			b.WriteString(" " + f(r) + " |")
		}
		b.WriteString("\n")
	}
	// awxnext answers with the AWX version its API follows.
	row("API version (ping)", func(r *Result) string { return r.Version })
	row("Event ingestion (events/s)", func(r *Result) string {
		return fmt.Sprintf("%.0f (%d events in %.1f s)", r.EventsPerSecond, r.Events, r.EventSeconds)
	})
	ms := func(l Latency) string { return fmt.Sprintf("%.0f / %.0f ms", l.P50, l.P95) }
	row("Hosts list, page of 200 (p50 / p95)", func(r *Result) string { return ms(r.HostList) + fmt.Sprintf(" (%d hosts)", r.Hosts) })
	row("Hosts search (p50 / p95)", func(r *Result) string { return ms(r.HostSearch) })
	row("Jobs list, page of 50 (p50 / p95)", func(r *Result) string { return ms(r.JobList) })
	row("Launches accepted (per s)", func(r *Result) string { return fmt.Sprintf("%.1f", r.LaunchesPerSecond) })
	row("Small jobs finished (per min)", func(r *Result) string { return fmt.Sprintf("%.1f (%d jobs)", r.JobsPerMinute, r.Launches) })
	return b.String()
}

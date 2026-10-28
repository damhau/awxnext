package metrics

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

// AWX's subsystem metrics: the per-node counters and gauges of its task
// manager, dependency and workflow managers, dispatcher and callback
// receiver, served by /api/v2/metrics/ with a node label. awxnext feeds
// them from its dispatcher pass, workflow engine and event writer; each
// node publishes its values (Snapshot) for the others to serve.

// SubKind is how a subsystem metric is kept.
type SubKind int

const (
	// SubCounter adds up from startup (AWX's IntM and FloatM).
	SubCounter SubKind = iota
	// SubGauge is the last value set (AWX's SetIntM and SetFloatM).
	SubGauge
	// SubHistogram counts observations in buckets (AWX's HistogramM).
	SubHistogram
)

// SubDef describes a subsystem metric.
type SubDef struct {
	Name, Help string
	Kind       SubKind
	Buckets    []float64
}

// SubsystemDefs are AWX 24.6's subsystem metrics, in its order.
var SubsystemDefs = []SubDef{
	{"subsystem_metrics_pipe_execute_seconds", "Time spent saving metrics to redis", SubCounter, nil},
	{"subsystem_metrics_pipe_execute_calls", "Number of calls to pipe_execute", SubCounter, nil},
	{"subsystem_metrics_send_metrics_seconds", "Time spent sending metrics to other nodes", SubCounter, nil},
	{"task_manager_get_tasks_seconds", "Time spent in loading tasks from db", SubGauge, nil},
	{"task_manager_start_task_seconds", "Time spent starting task", SubGauge, nil},
	{"task_manager_process_running_tasks_seconds", "Time spent processing running tasks", SubGauge, nil},
	{"task_manager_process_pending_tasks_seconds", "Time spent processing pending tasks", SubGauge, nil},
	{"task_manager__schedule_seconds", "Time spent in running the entire _schedule", SubGauge, nil},
	{"task_manager__schedule_calls", "Number of calls to _schedule, after lock is acquired", SubCounter, nil},
	{"task_manager_recorded_timestamp", "Unix timestamp when metrics were last recorded", SubGauge, nil},
	{"task_manager_tasks_started", "Number of tasks started", SubGauge, nil},
	{"task_manager_running_processed", "Number of running tasks processed", SubGauge, nil},
	{"task_manager_pending_processed", "Number of pending tasks processed", SubGauge, nil},
	{"task_manager_tasks_blocked", "Number of tasks blocked from running", SubGauge, nil},
	{"task_manager_commit_seconds", "Time spent in db transaction, including on_commit calls", SubGauge, nil},
	{"dependency_manager_get_tasks_seconds", "Time spent loading pending tasks from db", SubGauge, nil},
	{"dependency_manager_generate_dependencies_seconds", "Time spent generating dependencies for pending tasks", SubGauge, nil},
	{"dependency_manager__schedule_seconds", "Time spent in running the entire _schedule", SubGauge, nil},
	{"dependency_manager__schedule_calls", "Number of calls to _schedule, after lock is acquired", SubCounter, nil},
	{"dependency_manager_recorded_timestamp", "Unix timestamp when metrics were last recorded", SubGauge, nil},
	{"dependency_manager_pending_processed", "Number of pending tasks processed", SubGauge, nil},
	{"workflow_manager__schedule_seconds", "Time spent in running the entire _schedule", SubGauge, nil},
	{"workflow_manager__schedule_calls", "Number of calls to _schedule, after lock is acquired", SubCounter, nil},
	{"workflow_manager_recorded_timestamp", "Unix timestamp when metrics were last recorded", SubGauge, nil},
	{"workflow_manager_spawn_workflow_graph_jobs_seconds", "Time spent spawning workflow tasks", SubGauge, nil},
	{"workflow_manager_get_tasks_seconds", "Time spent loading workflow tasks from db", SubGauge, nil},
	{"dispatcher_pool_scale_up_events", "Number of times local dispatcher scaled up a worker since startup", SubGauge, nil},
	{"dispatcher_pool_active_task_count", "Number of active tasks in the worker pool when last task was submitted", SubGauge, nil},
	{"dispatcher_pool_max_worker_count", "Highest number of workers in worker pool in last collection interval, about 20s", SubGauge, nil},
	{"dispatcher_availability", "Fraction of time (in last collection interval) dispatcher was able to receive messages", SubGauge, nil},
	{"callback_receiver_events_queue_size_redis", "Current number of events in redis queue", SubGauge, nil},
	{"callback_receiver_events_popped_redis", "Number of events popped from redis", SubCounter, nil},
	{"callback_receiver_events_in_memory", "Current number of events in memory (in transfer from redis to db)", SubCounter, nil},
	{"callback_receiver_batch_events_errors", "Number of times batch insertion failed", SubCounter, nil},
	{"callback_receiver_events_insert_db_seconds", "Total time spent saving events to database", SubCounter, nil},
	{"callback_receiver_events_insert_db", "Number of events batch inserted into database", SubCounter, nil},
	{"callback_receiver_events_broadcast", "Number of events broadcast to other control plane nodes", SubCounter, nil},
	{"callback_receiver_batch_events_insert_db", "Number of events batch inserted into database", SubHistogram, []float64{10, 50, 150, 350, 650, 2000}},
	{"callback_receiver_event_processing_avg_seconds", "Average processing time per event per callback receiver batch", SubGauge, nil},
}

type subHist struct {
	Counts []float64 `json:"counts"`
	Sum    float64   `json:"sum"`
	Inf    float64   `json:"inf"`
}

// Subsystem holds this node's subsystem metric values.
type Subsystem struct {
	mu    sync.Mutex
	vals  map[string]float64
	hists map[string]*subHist
}

// Sub is this node's subsystem metrics.
var Sub = &Subsystem{vals: map[string]float64{}, hists: map[string]*subHist{}}

// Add adds to a counter, or to a gauge's current value.
func (s *Subsystem) Add(name string, v float64) {
	s.mu.Lock()
	s.vals[name] += v
	s.mu.Unlock()
}

// Set sets a gauge.
func (s *Subsystem) Set(name string, v float64) {
	s.mu.Lock()
	s.vals[name] = v
	s.mu.Unlock()
}

// Get returns a metric's current value.
func (s *Subsystem) Get(name string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vals[name]
}

// Observe records a value in a histogram.
func (s *Subsystem) Observe(name string, v float64) {
	var buckets []float64
	for _, d := range SubsystemDefs {
		if d.Name == name {
			buckets = d.Buckets
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hists[name]
	if h == nil {
		h = &subHist{Counts: make([]float64, len(buckets))}
		s.hists[name] = h
	}
	for i, b := range buckets {
		if v <= b {
			h.Counts[i]++
			break
		}
	}
	h.Sum += v
	h.Inf++
}

// Snapshot returns the values to publish: a number per metric, and
// {"counts", "sum", "inf"} per histogram, as AWX stores them.
func (s *Subsystem) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for k, v := range s.vals {
		out[k] = v
	}
	for k, h := range s.hists {
		out[k] = map[string]any{"counts": append([]float64{}, h.Counts...), "sum": h.Sum, "inf": h.Inf}
	}
	return out
}

// WriteSubsystem writes the subsystem metrics of nodes (node -> snapshot)
// as AWX does, keeping only the metrics named in only (all when empty).
func WriteSubsystem(w io.Writer, nodes map[string]map[string]any, only []string) {
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	keep := map[string]bool{}
	for _, m := range only {
		keep[m] = true
	}
	num := func(v any) float64 {
		f, _ := v.(float64)
		return f
	}
	for _, d := range SubsystemDefs {
		if len(keep) > 0 && !keep[d.Name] {
			continue
		}
		typ := "gauge"
		if d.Kind == SubHistogram {
			typ = "histogram"
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", d.Name, escapeHelp(d.Help), d.Name, typ)
		for _, n := range names {
			node := escapeValue(n)
			if d.Kind != SubHistogram {
				fmt.Fprintf(w, "%s{node=\"%s\"} %s\n", d.Name, node, FormatValue(num(nodes[n][d.Name])))
				continue
			}
			h, _ := nodes[n][d.Name].(map[string]any)
			counts, _ := h["counts"].([]any)
			var cum float64
			for i, b := range d.Buckets {
				if i < len(counts) {
					cum += num(counts[i])
				}
				fmt.Fprintf(w, "%s_bucket{le=\"%s\",node=\"%s\"} %s\n", d.Name, FormatValue(b), node, FormatValue(cum))
			}
			fmt.Fprintf(w, "%s_bucket{le=\"+Inf\",node=\"%s\"} %s\n", d.Name, node, FormatValue(num(h["inf"])))
			fmt.Fprintf(w, "%s_count{node=\"%s\"} %s\n", d.Name, node, FormatValue(num(h["inf"])))
			fmt.Fprintf(w, "%s_sum{node=\"%s\"} %s\n", d.Name, node, FormatValue(num(h["sum"])))
		}
	}
}

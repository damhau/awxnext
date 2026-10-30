package jobs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/logship"
	"github.com/damhau/awxnext/internal/metrics"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one job event (an ansible-runner event or a synthetic output line).
type Event struct {
	Counter    int            `json:"counter"`
	Event      string         `json:"event"`
	UUID       string         `json:"uuid"`
	ParentUUID string         `json:"parent_uuid"`
	Created    time.Time      `json:"created"`
	Stdout     string         `json:"stdout"`
	StartLine  int            `json:"start_line"`
	EndLine    int            `json:"end_line"`
	Verbosity  int            `json:"verbosity"`
	HostName   string         `json:"host_name"`
	HostID     *int64         `json:"host"`
	Playbook   string         `json:"playbook"`
	Play       string         `json:"play"`
	Role       string         `json:"role"`
	Task       string         `json:"task"`
	Failed     bool           `json:"failed"`
	Changed    bool           `json:"changed"`
	EventData  map[string]any `json:"event_data"`
}

// Output collects events for one job and writes them to the database in
// batches with COPY. It is safe for concurrent use.
type Output struct {
	// Kind is the job's type, naming its field in events sent to the
	// external log aggregator (job, project_update, ...).
	Kind string
	// RateLimit is how many events a second the job stores (0: no limit):
	// beyond it, consecutive verbose events are folded into one, so a noisy
	// job cannot starve the others. Set before the first Emit.
	RateLimit  int
	db         *pgxpool.Pool
	jobID      int64
	jobCreated time.Time
	hostIDs    map[string]int64
	publish    func(Event)
	onFlush    func(maxCounter int)

	mu      sync.Mutex
	buf     []Event
	counter int
	line    int
	total   int
	flushed chan struct{}
	done    chan struct{}
	stopped chan struct{}
	err     error

	// The rate limit's token bucket, and how many counted events were
	// folded: stored counters are the emitted ones less that, so they stay
	// contiguous.
	tokens  float64
	refill  time.Time
	shifted int
	folded  int
}

// foldMax bounds the output a folded event holds; past it, a new one starts.
const foldMax = 64 << 10

const (
	flushEvery = 250 * time.Millisecond
	flushSize  = 500
)

// NewOutput starts a writer. hostIDs maps inventory host names to ids for linking events.
// onFlush is called with the highest stored counter after each batch is written.
func NewOutput(db *pgxpool.Pool, jobID int64, jobCreated time.Time, hostIDs map[string]int64, publish func(Event), onFlush func(int)) *Output {
	o := &Output{db: db, jobID: jobID, jobCreated: jobCreated, hostIDs: hostIDs, publish: publish, onFlush: onFlush,
		flushed: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	go o.loop()
	return o
}

// Emit records an event. Counter and line numbers are assigned when missing.
func (o *Output) Emit(e Event) {
	o.mu.Lock()
	if e.Created.IsZero() {
		e.Created = time.Now()
	}
	lines := 0
	if e.Stdout != "" {
		lines = strings.Count(e.Stdout, "\n") + 1
	}
	if e.EndLine == 0 && e.StartLine == 0 {
		e.StartLine = o.line
		e.EndLine = o.line + lines
	}
	if e.EndLine > o.line {
		o.line = e.EndLine
	}
	if o.fold(e) {
		// The previous verbose event took this one's output: viewers get it
		// again, whole, under its counter.
		merged := o.buf[len(o.buf)-1]
		o.mu.Unlock()
		metrics.Sub.Add("callback_receiver_events_popped_redis", 1)
		if o.publish != nil {
			o.publish(merged)
		}
		return
	}
	if e.Counter == 0 {
		o.counter++
		e.Counter = o.counter
	} else if e.Counter -= o.shifted; e.Counter > o.counter {
		o.counter = e.Counter
	}
	if e.HostID == nil && e.HostName != "" {
		if id, ok := o.hostIDs[e.HostName]; ok {
			e.HostID = &id
		}
	}
	o.buf = append(o.buf, e)
	full := len(o.buf) >= flushSize
	o.mu.Unlock()
	// AWX's callback receiver metrics: events received, then held until stored.
	metrics.Sub.Add("callback_receiver_events_popped_redis", 1)
	metrics.Sub.Add("callback_receiver_events_in_memory", 1)
	if o.publish != nil {
		o.publish(e)
		metrics.Sub.Add("callback_receiver_events_broadcast", 1)
	}
	if full {
		select {
		case o.flushed <- struct{}{}:
		default:
		}
	}
}

// fold takes a verbose event into the previous one, still buffered, when
// the job is over its RateLimit; o.mu is held.
func (o *Output) fold(e Event) bool {
	if o.RateLimit <= 0 {
		return false
	}
	now := time.Now()
	if o.refill.IsZero() {
		o.tokens, o.refill = float64(o.RateLimit), now
	}
	o.tokens = min(float64(o.RateLimit), o.tokens+now.Sub(o.refill).Seconds()*float64(o.RateLimit))
	o.refill = now
	if o.tokens >= 1 {
		o.tokens--
		return false
	}
	n := len(o.buf)
	if e.Event != "verbose" || n == 0 || o.buf[n-1].Event != "verbose" || len(o.buf[n-1].Stdout)+len(e.Stdout) > foldMax {
		// Other events are always stored, and count against the budget.
		o.tokens--
		return false
	}
	last := &o.buf[n-1]
	if last.Stdout != "" && e.Stdout != "" {
		last.Stdout += "\n"
	}
	last.Stdout += e.Stdout
	last.EndLine = e.EndLine
	if e.Counter > 0 {
		o.shifted++
	}
	o.folded++
	return true
}

// Folded returns how many events were folded into others.
func (o *Output) Folded() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.folded
}

// Line emits a plain output line as a verbose event (used by native runners).
func (o *Output) Line(s string) {
	o.Emit(Event{Event: "verbose", Stdout: s})
}

func (o *Output) loop() {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-o.flushed:
		case <-o.done:
			o.flush()
			close(o.stopped)
			return
		}
		o.flush()
	}
}

func (o *Output) flush() {
	o.mu.Lock()
	batch := o.buf
	o.buf = nil
	o.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	rows := make([][]any, len(batch))
	for i, e := range batch {
		data := e.EventData
		if data == nil {
			data = map[string]any{}
		}
		rows[i] = []any{o.jobID, o.jobCreated, e.Created, e.Created, e.Counter, e.Event, data, e.Failed, e.Changed,
			e.UUID, e.ParentUUID, e.HostID, e.HostName, e.Playbook, e.Play, e.Role, e.Task, e.Stdout, e.StartLine, e.EndLine, e.Verbosity}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	defer metrics.Sub.Add("callback_receiver_events_in_memory", -float64(len(batch)))
	_, err := o.db.CopyFrom(ctx, pgx.Identifier{"job_events"},
		[]string{"job_id", "job_created", "created", "modified", "counter", "event", "event_data", "failed", "changed",
			"uuid", "parent_uuid", "host_id", "host_name", "playbook", "play", "role", "task", "stdout", "start_line", "end_line", "verbosity"},
		pgx.CopyFromRows(rows))
	secs := time.Since(start).Seconds()
	if err != nil {
		metrics.Sub.Add("callback_receiver_batch_events_errors", 1)
		slog.Error("writing job events", "job", o.jobID, "err", err)
		o.mu.Lock()
		o.err = err
		o.mu.Unlock()
		return
	}
	o.mu.Lock()
	o.total += len(batch)
	total := o.total
	o.mu.Unlock()
	eventsWritten.With().Add(float64(len(batch)))
	metrics.Sub.Add("callback_receiver_events_insert_db", float64(len(batch)))
	metrics.Sub.Add("callback_receiver_events_insert_db_seconds", secs)
	metrics.Sub.Observe("callback_receiver_batch_events_insert_db", float64(len(batch)))
	metrics.Sub.Set("callback_receiver_event_processing_avg_seconds", secs/float64(len(batch)))
	o.logEvents(ctx, batch)
	o.db.Exec(ctx, "UPDATE unified_jobs SET emitted_events = $2 WHERE id = $1", o.jobID, total)
	if o.onFlush != nil {
		maxCounter := 0
		for _, e := range batch {
			maxCounter = max(maxCounter, e.Counter)
		}
		o.onFlush(maxCounter)
	}
}

// logEvents sends stored events to the external log aggregator, as AWX's
// awx.analytics.job_events logger: "Event data saved." with the event's
// fields.
func (o *Output) logEvents(ctx context.Context, batch []Event) {
	if !logship.Takes(ctx, "awx.analytics.job_events", slog.LevelInfo) {
		return
	}
	kind := o.Kind
	if kind == "" {
		kind = "job"
	}
	for _, e := range batch {
		data := e.EventData
		if data == nil {
			data = map[string]any{}
		}
		logship.Emit(ctx, "awx.analytics.job_events", slog.LevelInfo, "Event data saved.", map[string]any{
			kind: o.jobID, "job_created": o.jobCreated, "created": e.Created, "modified": e.Created, "event": e.Event, "event_data": data,
			"failed": e.Failed, "changed": e.Changed, "uuid": e.UUID, "parent_uuid": e.ParentUUID, "host": e.HostID, "host_name": e.HostName,
			"playbook": e.Playbook, "play": e.Play, "role": e.Role, "task": e.Task, "counter": e.Counter, "stdout": e.Stdout,
			"start_line": e.StartLine, "end_line": e.EndLine, "verbosity": e.Verbosity, "event_display": EventDisplay(e.Event)})
	}
}

// Close flushes remaining events and marks event processing finished.
func (o *Output) Close() error {
	close(o.done)
	<-o.stopped
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o.mu.Lock()
	total := o.total
	err := o.err
	final := o.counter
	o.mu.Unlock()
	o.db.Exec(ctx, "UPDATE unified_jobs SET emitted_events = $2, event_processing_finished = true WHERE id = $1", o.jobID, total)
	// AWX's jobs-summary, sent when a job's output ends (its EOF event).
	api.Broadcast(ctx, o.db, "jobs-summary", map[string]any{"group_name": "jobs", "unified_job_id": o.jobID, "final_counter": final})
	return err
}

// Position returns the last event counter, as emitted (before folding),
// and output line, so a second execution streamed into the same job can
// continue after them.
func (o *Output) Position() (counter, line int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counter + o.shifted, o.line
}

// Counter returns the last assigned event counter.
func (o *Output) Counter() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counter
}

package jobs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	db         *pgxpool.Pool
	jobID      int64
	jobCreated time.Time
	hostIDs    map[string]int64
	publish    func(Event)

	mu      sync.Mutex
	buf     []Event
	counter int
	line    int
	total   int
	flushed chan struct{}
	done    chan struct{}
	stopped chan struct{}
	err     error
}

const (
	flushEvery = 250 * time.Millisecond
	flushSize  = 500
)

// NewOutput starts a writer. hostIDs maps inventory host names to ids for linking events.
func NewOutput(db *pgxpool.Pool, jobID int64, jobCreated time.Time, hostIDs map[string]int64, publish func(Event)) *Output {
	o := &Output{db: db, jobID: jobID, jobCreated: jobCreated, hostIDs: hostIDs, publish: publish,
		flushed: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	go o.loop()
	return o
}

// Emit records an event. Counter and line numbers are assigned when missing.
func (o *Output) Emit(e Event) {
	o.mu.Lock()
	if e.Counter == 0 {
		o.counter++
		e.Counter = o.counter
	} else if e.Counter > o.counter {
		o.counter = e.Counter
	}
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
	if e.HostID == nil && e.HostName != "" {
		if id, ok := o.hostIDs[e.HostName]; ok {
			e.HostID = &id
		}
	}
	o.buf = append(o.buf, e)
	full := len(o.buf) >= flushSize
	o.mu.Unlock()
	if o.publish != nil {
		o.publish(e)
	}
	if full {
		select {
		case o.flushed <- struct{}{}:
		default:
		}
	}
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
	_, err := o.db.CopyFrom(ctx, pgx.Identifier{"job_events"},
		[]string{"job_id", "job_created", "created", "modified", "counter", "event", "event_data", "failed", "changed",
			"uuid", "parent_uuid", "host_id", "host_name", "playbook", "play", "role", "task", "stdout", "start_line", "end_line", "verbosity"},
		pgx.CopyFromRows(rows))
	if err != nil {
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
	o.db.Exec(ctx, "UPDATE unified_jobs SET emitted_events = $2 WHERE id = $1", o.jobID, total)
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
	o.mu.Unlock()
	o.db.Exec(ctx, "UPDATE unified_jobs SET emitted_events = $2, event_processing_finished = true WHERE id = $1", o.jobID, total)
	return err
}

// Position returns the last assigned event counter and output line, so a
// second execution streamed into the same job can continue after them.
func (o *Output) Position() (counter, line int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counter, o.line
}

// Counter returns the last assigned event counter.
func (o *Output) Counter() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counter
}

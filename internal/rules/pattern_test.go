package rules

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type patternRun struct {
	t     *testing.T
	sim   *Simulator
	acted [][]int
	at    []time.Time
}

func newPatternRun(t *testing.T, r SimRule) *patternRun {
	pr := &patternRun{t: t}
	if r.Filter == "" {
		r.Filter = "true"
	}
	r.Key = "event.data.host"
	pr.sim = NewSimulator(r, func(ms []Member, at time.Time) Outcome {
		var idx []int
		for _, m := range ms {
			idx = append(idx, m.Index)
		}
		pr.acted = append(pr.acted, idx)
		pr.at = append(pr.at, at)
		return Outcome{Name: Fired}
	})
	return pr
}

func (pr *patternRun) add(after time.Duration, typ, host string) {
	pr.sim.Add(context.Background(), SimEvent{Received: t0.Add(after),
		Value: map[string]any{"type": typ, "data": map[string]any{"host": host}}})
}

func (pr *patternRun) want(outcomes ...string) {
	pr.t.Helper()
	var got []string
	for _, d := range pr.sim.Decisions {
		got = append(got, d.Outcome)
	}
	if !slices.Equal(got, outcomes) {
		for _, d := range pr.sim.Decisions {
			pr.t.Logf("%d %s: %s", d.Index, d.Outcome, d.Reason)
		}
		pr.t.Fatalf("outcomes %v, want %v", got, outcomes)
	}
}

func (pr *patternRun) wantActed(want ...[]int) {
	pr.t.Helper()
	if fmt.Sprint(pr.acted) != fmt.Sprint(want) {
		pr.t.Fatalf("acted %v, want %v", pr.acted, want)
	}
}

func TestPatternCount(t *testing.T) {
	pr := newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternCount, Count: 3, Window: time.Minute}})
	pr.add(0, "x", "a")
	pr.add(10*time.Second, "x", "a")
	pr.add(15*time.Second, "x", "b")
	pr.add(20*time.Second, "x", "a")
	pr.add(30*time.Second, "x", "a")
	pr.want(Fired, Fired, Waiting, Fired, Waiting)
	pr.wantActed([]int{0, 1, 3})
	if !pr.at[0].Equal(t0.Add(20 * time.Second)) {
		t.Errorf("fired at %s", pr.at[0])
	}
	d := pr.sim.Decisions[4]
	p := d.Details["pattern"].(map[string]any)
	if p["seen"] != 1 || p["needed"] != 3 || d.Reason != "1 of 3 events within the window: waiting for 2 more before 2026-01-01T00:01:30Z." {
		t.Errorf("progress %v: %s", p, d.Reason)
	}

	// The window slides: the first event leaves it before the third comes.
	pr = newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternCount, Count: 3, Window: time.Minute}})
	pr.add(0, "x", "c")
	pr.add(50*time.Second, "x", "c")
	pr.add(70*time.Second, "x", "c")
	pr.add(80*time.Second, "x", "c")
	pr.want(Expired, Fired, Fired, Fired)
	pr.wantActed([]int{1, 2, 3})

	// An event received before the window of the key's newest is late.
	pr = newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternCount, Count: 2, Window: time.Minute}})
	pr.add(100*time.Second, "x", "a")
	pr.add(30*time.Second, "x", "a")
	pr.add(50*time.Second, "x", "a")
	pr.want(Fired, Expired, Fired)
	pr.wantActed([]int{2, 0})
}

func TestPatternAll(t *testing.T) {
	pr := newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternAll, Conditions: 3, Window: time.Minute},
		Conditions: []string{`event.type == "disk"`, `event.type == "cpu"`, `event.type == "mem"`}})
	pr.add(0, "mem", "a")
	pr.add(5*time.Second, "mem", "a")
	pr.add(10*time.Second, "net", "a")
	pr.add(20*time.Second, "disk", "a")
	pr.add(30*time.Second, "cpu", "a")
	pr.want(Fired, Fired, NotMatched, Fired, Fired)
	pr.wantActed([]int{0, 1, 3, 4})
	if d := pr.sim.Decisions[2]; d.Details["conditions"] == nil || len(d.Details["conditions"].([]map[string]any)) != 3 {
		t.Errorf("not matched details %v", d.Details)
	}

	pr = newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternAll, Conditions: 2, Window: time.Minute},
		Conditions: []string{`event.type == "disk"`, `event.type == "cpu"`}})
	pr.add(0, "disk", "a")
	if r := pr.sim.Decisions[0].Reason; r != "1 of 2 conditions matched: waiting for condition 2 before 2026-01-01T00:01:00Z." {
		t.Errorf("reason %q", r)
	}
	pr.add(90*time.Second, "cpu", "a")
	pr.want(Expired, Waiting)
}

func TestPatternSequence(t *testing.T) {
	pr := newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternSequence, Conditions: 3, Window: time.Minute},
		Conditions: []string{`event.type == "a"`, `event.type == "b"`, `event.type == "c"`}})
	pr.add(0, "b", "h")
	pr.add(5*time.Second, "a", "h")
	pr.add(6*time.Second, "c", "h")
	pr.add(10*time.Second, "b", "h")
	pr.add(20*time.Second, "c", "h")
	pr.want(Ignored, Fired, Ignored, Fired, Fired)
	pr.wantActed([]int{1, 3, 4})

	// The window is the first step's: the third step comes too late.
	pr = newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternSequence, Conditions: 3, Window: time.Minute},
		Conditions: []string{`event.type == "a"`, `event.type == "b"`, `event.type == "c"`}})
	pr.add(100*time.Second, "a", "h")
	pr.add(130*time.Second, "b", "h")
	pr.add(170*time.Second, "c", "h")
	pr.want(Expired, Expired, Ignored)
	if r := pr.sim.Decisions[1].Reason; r != "The window ended before the pattern was complete." {
		t.Errorf("reason %q", r)
	}

	// A later first step starts a sequence of its own.
	pr = newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternSequence, Conditions: 2, Window: time.Minute},
		Conditions: []string{`event.type == "a"`, `event.type == "b"`}})
	pr.add(0, "a", "h")
	pr.add(50*time.Second, "a", "h")
	pr.add(80*time.Second, "b", "h")
	pr.want(Expired, Fired, Fired)
	pr.wantActed([]int{1, 2})
}

func TestPatternAbsence(t *testing.T) {
	rule := SimRule{Pattern: Pattern{Kind: PatternAbsence, Conditions: 2, Window: time.Minute},
		Conditions: []string{`event.type == "down"`, `event.type == "up"`}}
	pr := newPatternRun(t, rule)
	pr.add(0, "down", "a")
	pr.add(10*time.Second, "down", "a")
	pr.add(30*time.Second, "up", "a")
	pr.add(100*time.Second, "down", "a")
	pr.add(110*time.Second, "down", "b")
	pr.add(150*time.Second, "up", "b")
	pr.want(Cleared, Cleared, Cleared, Waiting, Cleared, Cleared)
	pr.sim.Finish(t0.Add(time.Hour))
	pr.add(200*time.Second, "up", "a")
	pr.want(Cleared, Cleared, Cleared, Fired, Cleared, Cleared, Ignored)
	pr.wantActed([]int{3})
	if !pr.at[0].Equal(t0.Add(160 * time.Second)) {
		t.Errorf("fired at %s", pr.at[0])
	}

	// A window that ends fires before a later event of another key.
	pr = newPatternRun(t, rule)
	pr.add(0, "down", "a")
	pr.add(100*time.Second, "down", "b")
	pr.want(Fired, Waiting)
	if !pr.at[0].Equal(t0.Add(time.Minute)) {
		t.Errorf("fired at %s", pr.at[0])
	}

	// An event of the second condition before the first doesn't clear it.
	pr = newPatternRun(t, rule)
	pr.add(10*time.Second, "down", "a")
	pr.add(5*time.Second, "up", "a")
	pr.sim.Finish(t0.Add(30 * time.Second))
	pr.want(Waiting, Ignored)
	pr.sim.Finish(t0.Add(70 * time.Second))
	pr.want(Fired, Ignored)
}

func TestPatternBreakerCountsFirings(t *testing.T) {
	pr := newPatternRun(t, SimRule{Pattern: Pattern{Kind: PatternCount, Count: 2, Window: time.Minute},
		BreakerFirings: 2, BreakerWindow: time.Hour})
	for i := range 5 {
		pr.add(time.Duration(i)*time.Second, "x", "a")
	}
	pr.want(Fired, Fired, Fired, Fired, Waiting)
	if _, ok := pr.sim.Tripped["a"]; !ok {
		t.Fatal("breaker not tripped after two firings")
	}
	pr.add(10*time.Second, "x", "a")
	pr.want(Fired, Fired, Fired, Fired, Held, Held)
}

func TestPatternLimit(t *testing.T) {
	p := Pattern{Kind: PatternCount, Count: MaxPatternEvents + 1, Window: time.Hour}
	s := &PatternState{}
	for i := range MaxPatternEvents {
		if st := p.Add(s, PatternEvent{ID: int64(i), Received: t0.Add(time.Duration(i) * time.Millisecond)}); st.Outcome != Waiting {
			t.Fatalf("event %d: %s", i, st.Outcome)
		}
	}
	if st := p.Add(s, PatternEvent{Received: t0.Add(time.Second)}); st.Outcome != Deduplicated {
		t.Fatalf("over the limit: %s", st.Outcome)
	}
	if d := p.Deadline(s); !d.Equal(t0.Add(time.Hour)) {
		t.Errorf("deadline %s", d)
	}
}

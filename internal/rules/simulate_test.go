package rules

import (
	"context"
	"testing"
	"time"
)

func TestSimulatorOnceAfter(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var acted [][]int
	sim := NewSimulator(SimRule{Filter: `event.type == "x"`, Key: `event.data.host`, Throttle: OnceAfter, Window: time.Minute},
		func(ms []Member, at time.Time) Outcome {
			var idx []int
			for _, m := range ms {
				idx = append(idx, m.Index)
			}
			acted = append(acted, idx)
			if !at.Equal(start.Add(time.Minute)) && !at.Equal(start.Add(3*time.Minute)) {
				t.Errorf("acted at %s", at)
			}
			return Outcome{Name: Fired}
		})
	ev := func(after time.Duration, typ, host string) {
		sim.Add(context.Background(), SimEvent{Received: start.Add(after),
			Value: map[string]any{"type": typ, "data": map[string]any{"host": host}}})
	}
	ev(0, "x", "a")
	ev(10*time.Second, "x", "a")
	ev(20*time.Second, "y", "a")
	ev(30*time.Second, "x", "b")
	ev(2*time.Minute, "x", "a")
	sim.Finish(start.Add(2 * time.Minute))
	want := []string{Fired, Deduplicated, NotMatched, Fired, Waiting}
	for i, d := range sim.Decisions {
		if d.Outcome != want[i] {
			t.Errorf("decision %d: %s, want %s (%s)", i, d.Outcome, want[i], d.Reason)
		}
	}
	if len(acted) != 1 || len(acted[0]) != 2 || acted[0][0] != 0 || acted[0][1] != 3 {
		t.Fatalf("acted %v", acted)
	}
	if m := sim.Decisions[2].Miss; m == nil || m.Expression != `event.type == "x"` {
		t.Errorf("miss %+v", m)
	}
	sim.Finish(start.Add(3 * time.Minute))
	if sim.Decisions[4].Outcome != Fired || len(acted) != 2 {
		t.Errorf("the second window: %+v, acted %v", sim.Decisions[4], acted)
	}
}

func TestSimulatorOnceWithinAndBreaker(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hold := true
	sim := NewSimulator(SimRule{Filter: "true", Throttle: OnceWithin, Window: time.Hour, BreakerFirings: 2, BreakerWindow: 24 * time.Hour},
		func(ms []Member, at time.Time) Outcome {
			if hold {
				hold = false
				return Outcome{Name: Held, Guardrail: "max_concurrent_runs"}
			}
			return Outcome{Name: Fired}
		})
	for _, h := range []time.Duration{0, time.Minute, 2 * time.Minute, 3 * time.Hour, 5 * time.Hour} {
		sim.Add(context.Background(), SimEvent{Received: start.Add(h), Value: map[string]any{}})
	}
	want := []string{Held, Fired, Throttled, Fired, Held}
	for i, d := range sim.Decisions {
		if d.Outcome != want[i] {
			t.Errorf("decision %d: %s, want %s (%s)", i, d.Outcome, want[i], d.Reason)
		}
	}
	if g := sim.Decisions[4].Guardrail; g != "circuit_breaker" {
		t.Errorf("guardrail %q", g)
	}
	if at, ok := sim.Tripped[""]; !ok || !at.Equal(start.Add(3*time.Hour)) {
		t.Errorf("tripped %v", sim.Tripped)
	}
}

func TestSimulatorChecks(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fail := map[string]bool{"c": true}
	sim := NewSimulator(SimRule{Filter: `event.type == "firing"`, Key: `event.host`, BreakerFirings: 2, BreakerWindow: time.Hour,
		Check: SimCheck{Condition: `event.type == "resolved"`, Window: 5 * time.Minute, RunTime: time.Minute}},
		func(ms []Member, at time.Time) Outcome {
			if fail[ms[0].Key] {
				return Outcome{Name: Failed}
			}
			return Outcome{Name: Fired}
		})
	ev := func(after time.Duration, typ, host string) {
		sim.Add(context.Background(), SimEvent{Received: start.Add(after), Value: map[string]any{"type": typ, "host": host}})
	}
	ev(0, "firing", "a")
	ev(0, "firing", "b")
	ev(0, "firing", "c")
	ev(2*time.Minute, "resolved", "a")
	ev(7*time.Minute, "resolved", "b")
	ev(8*time.Minute, "firing", "b")
	ev(9*time.Minute, "firing", "d")
	sim.Finish(start.Add(10 * time.Minute))
	want := []string{CheckFixed, CheckNotFixed, CheckActionFailed, "", "", "", CheckPending}
	for i, d := range sim.Decisions {
		if d.Check != want[i] {
			t.Errorf("decision %d: check %q, want %q", i, d.Check, want[i])
		}
	}
	if r := sim.Decisions[0].ResolvedBy; r == nil || *r != 3 {
		t.Errorf("resolved by %v", r)
	}
	// b fired, and its fix didn't work: the breaker counts two firings.
	if d := sim.Decisions[5]; d.Outcome != Held || d.Guardrail != "circuit_breaker" {
		t.Errorf("b's second firing: %+v", d)
	}
	if at, ok := sim.Tripped["b"]; !ok || !at.Equal(start.Add(6*time.Minute)) {
		t.Errorf("tripped %v", sim.Tripped)
	}
}

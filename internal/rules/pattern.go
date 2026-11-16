package rules

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// A pattern makes a rule wait, for each key, for several events within a
// window before it acts. Windows count by the time events were received:
// the window of a key has a clock, its newest event's received time, or
// the time the timer moved it to, and an event older than the window by
// that clock is too late to join it. The engine keeps each key's window in
// the database and the simulator in memory; both move it with Add and
// Advance.

// The patterns a rule can wait for.
const (
	// PatternCount fires when a key has Count events within the window.
	PatternCount = "count"
	// PatternAll fires when a key has an event of each condition within
	// the window, in any order.
	PatternAll = "all"
	// PatternSequence fires when a key has an event of each condition, in
	// the conditions' order, within the window of the first.
	PatternSequence = "sequence"
	// PatternAbsence fires at the end of the window opened by an event of
	// the first condition, unless an event of the second came first.
	PatternAbsence = "absence"
)

// The outcomes of the events a window lets go of without firing.
const (
	// Expired: the window ended before the pattern was complete.
	Expired = "expired"
	// Cleared: the absence's second condition came in time.
	Cleared = "cleared"
	// Ignored: the event extends no window, as a sequence's later step
	// with no earlier one before it.
	Ignored = "ignored"
)

const (
	// MaxPatternEvents bounds the events a key's window holds.
	MaxPatternEvents = 1000
	// MaxConditions bounds a pattern's conditions.
	MaxConditions = 10
)

// Pattern is what a rule waits for on each key.
type Pattern struct {
	Kind string
	// Count is how many events a count waits for.
	Count int
	// Conditions is how many conditions the pattern has.
	Conditions int
	Window     time.Duration
}

// PatternEvent is an event a key's window holds.
type PatternEvent struct {
	// ID is the event's decision, or its index in a simulation.
	ID       int64     `json:"decision"`
	Stream   int64     `json:"stream,omitempty"`
	Sequence int64     `json:"sequence,omitempty"`
	Received time.Time `json:"received"`
	// Start is when the sequence the event extends started; its own
	// received time in the other patterns.
	Start time.Time `json:"start"`
	// Matched are the conditions the event matched; a sequence keeps the
	// step it fills.
	Matched []int `json:"matched,omitempty"`
}

// PatternState is a key's window.
type PatternState struct {
	Events []PatternEvent `json:"events"`
	// Opened is when the window's first event was received.
	Opened time.Time `json:"opened"`
	// Clock is the newest time the window saw.
	Clock time.Time `json:"clock"`
}

// PatternStep is what an event, or the time passing, did to a window.
type PatternStep struct {
	// Outcome is the event's: Waiting, Fired, Cleared, Ignored, Expired or
	// Deduplicated. Empty when only time passed.
	Outcome string
	Reason  string
	// Due are the events of an absence that ended before the event came:
	// the rule fires for them first.
	Due []PatternEvent
	// Fire are the events the rule fires for, in the order received: with
	// Outcome Fired, the event's among them.
	Fire []PatternEvent
	// Expired are the events the window let go of.
	Expired []PatternEvent
	// Cleared are the events of an absence that the event cleared.
	Cleared []PatternEvent
	// At is when the window fired: when the absence ended, or the window's
	// clock when it completed.
	At time.Time
}

// Conditions evaluates a pattern's conditions on an event: the indexes of
// those that hold, and why each of the others doesn't.
func Conditions(ctx context.Context, conds []string, v Vars) ([]int, []*Miss) {
	var matched []int
	misses := make([]*Miss, len(conds))
	for i, c := range conds {
		ok, m := Filter(ctx, c, v)
		if ok {
			matched = append(matched, i)
		} else {
			misses[i] = m
		}
	}
	return matched, misses
}

// ConditionsMiss is the reason and the details of an event that passed the
// filter but matched none of the conditions.
func ConditionsMiss(misses []*Miss) (string, map[string]any) {
	list := make([]map[string]any, 0, len(misses))
	for _, m := range misses {
		if m != nil {
			list = append(list, m.Details())
		}
	}
	return "The event matches none of the rule's conditions.", map[string]any{"conditions": list}
}

func (p Pattern) alive(s *PatternState, e PatternEvent) bool {
	return s.Clock.Before(e.Start.Add(p.Window))
}

// Advance moves the window's clock to now: the events too old for the
// window expire, and an absence whose window ended fires.
func (p Pattern) Advance(s *PatternState, now time.Time) PatternStep {
	if now.After(s.Clock) {
		s.Clock = now
	}
	var st PatternStep
	if len(s.Events) == 0 {
		return st
	}
	if p.Kind == PatternAbsence {
		if end := s.Opened.Add(p.Window); !s.Clock.Before(end) {
			st.Fire, st.At, s.Events = s.Events, end, nil
		}
		return st
	}
	keep := make([]PatternEvent, 0, len(s.Events))
	for _, e := range s.Events {
		if p.alive(s, e) {
			keep = append(keep, e)
		} else {
			st.Expired = append(st.Expired, e)
		}
	}
	s.Events = keep
	return st
}

// Add moves the window to the event's time and adds the event, which
// matched the conditions in ev.Matched (none for a count).
func (p Pattern) Add(s *PatternState, ev PatternEvent) PatternStep {
	st := p.Advance(s, ev.Received)
	if p.Kind == PatternAbsence {
		st.Due, st.Fire = st.Fire, nil
	}
	ev.Start = ev.Received
	if p.Kind != PatternAbsence && !p.alive(s, ev) {
		st.Outcome = Expired
		st.Reason = fmt.Sprintf("The event is older than the window: the key's newest event was received at %s.", stamp(s.Clock))
		return st
	}
	if len(s.Events) >= MaxPatternEvents {
		st.Outcome = Deduplicated
		st.Reason = fmt.Sprintf("The key's window already holds %d events.", MaxPatternEvents)
		return st
	}
	switch p.Kind {
	case PatternAbsence:
		p.addAbsence(s, ev, &st)
	case PatternSequence:
		p.addSequence(s, ev, &st)
	default:
		p.insert(s, ev)
		if p.complete(s) {
			st.Outcome, st.Fire, st.At, s.Events = Fired, s.Events, s.Clock, nil
			return st
		}
		st.Outcome, st.Reason = Waiting, p.waiting(s)
	}
	return st
}

func (p Pattern) insert(s *PatternState, ev PatternEvent) {
	i := sort.Search(len(s.Events), func(i int) bool { return s.Events[i].Received.After(ev.Received) })
	s.Events = slices.Insert(s.Events, i, ev)
	if p.Kind != PatternAbsence || len(s.Events) == 1 {
		s.Opened = s.Events[0].Received
	}
}

func (p Pattern) complete(s *PatternState) bool {
	if p.Kind == PatternCount {
		return len(s.Events) >= p.Count
	}
	return len(p.covered(s)) == p.Conditions
}

// covered are the conditions the window's events matched, in order, and
// until when each stays matched.
func (p Pattern) covered(s *PatternState) map[int]time.Time {
	out := map[int]time.Time{}
	for _, e := range s.Events {
		end := e.Start.Add(p.Window)
		for _, c := range e.Matched {
			if end.After(out[c]) {
				out[c] = end
			}
		}
	}
	return out
}

func (p Pattern) addSequence(s *PatternState, ev PatternEvent, st *PatternStep) {
	step := -1
	for i := len(ev.Matched) - 1; i >= 0 && step < 0; i-- {
		c := ev.Matched[i]
		if c == 0 {
			step = 0
			break
		}
		found := false
		for _, e := range s.Events {
			if e.Matched[0] == c-1 && !e.Received.After(ev.Received) && (!found || e.Start.After(ev.Start)) {
				ev.Start, found = e.Start, true
			}
		}
		if found {
			step = c
		}
	}
	if step < 0 {
		last := ev.Matched[len(ev.Matched)-1]
		st.Outcome = Ignored
		st.Reason = fmt.Sprintf("The event matches step %d, but no event of step %d came before it within the window.", last+1, last)
		return
	}
	ev.Matched = []int{step}
	p.insert(s, ev)
	if step == p.Conditions-1 {
		st.Outcome, st.Fire, st.At, s.Events = Fired, s.Events, s.Clock, nil
		return
	}
	st.Outcome, st.Reason = Waiting, p.waiting(s)
}

func (p Pattern) addAbsence(s *PatternState, ev PatternEvent, st *PatternStep) {
	if slices.Contains(ev.Matched, 1) && len(s.Events) > 0 && !ev.Received.Before(s.Opened) {
		st.Outcome, st.Cleared, s.Events = Cleared, s.Events, nil
		st.Reason = fmt.Sprintf("The event cleared the %s of condition 1 that waited for it.", plural(len(st.Cleared), "event"))
		return
	}
	if !slices.Contains(ev.Matched, 0) {
		st.Outcome, st.Reason = Ignored, "No event of condition 1 waits for an event of condition 2."
		return
	}
	ev.Matched = []int{0}
	p.insert(s, ev)
	st.Outcome, st.Reason = Waiting, p.waiting(s)
}

// Deadline is when the window next changes with time alone: when its
// absence ends, or when its oldest event leaves it. Zero: never.
func (p Pattern) Deadline(s *PatternState) time.Time {
	if len(s.Events) == 0 {
		return time.Time{}
	}
	if p.Kind == PatternAbsence {
		return s.Opened.Add(p.Window)
	}
	var first time.Time
	for _, e := range s.Events {
		if end := e.Start.Add(p.Window); first.IsZero() || end.Before(first) {
			first = end
		}
	}
	return first
}

// until is the end of the window's best chance to complete.
func (p Pattern) until(s *PatternState) time.Time {
	switch p.Kind {
	case PatternAll:
		var first time.Time
		for _, end := range p.covered(s) {
			if first.IsZero() || end.Before(first) {
				first = end
			}
		}
		return first
	case PatternSequence:
		top, end := p.furthest(s), time.Time{}
		for _, e := range s.Events {
			if e.Matched[0] == top && e.Start.Add(p.Window).After(end) {
				end = e.Start.Add(p.Window)
			}
		}
		return end
	}
	return p.Deadline(s)
}

// furthest is the furthest step a sequence's window reached.
func (p Pattern) furthest(s *PatternState) int {
	top := 0
	for _, e := range s.Events {
		top = max(top, e.Matched[0])
	}
	return top
}

// Progress is the window's progress, as the decisions' details keep it:
// seen of needed, and until when the window can complete.
func (p Pattern) Progress(s *PatternState) map[string]any {
	d := map[string]any{"kind": p.Kind, "events": len(s.Events)}
	switch p.Kind {
	case PatternCount:
		d["seen"], d["needed"] = len(s.Events), p.Count
	case PatternAll:
		cov := p.covered(s)
		seen := make([]int, 0, len(cov))
		for c := range cov {
			seen = append(seen, c+1)
		}
		sort.Ints(seen)
		d["seen"], d["needed"], d["conditions"] = len(seen), p.Conditions, seen
	case PatternSequence:
		seen := 0
		if len(s.Events) > 0 {
			seen = p.furthest(s) + 1
		}
		d["seen"], d["needed"] = seen, p.Conditions
	}
	if len(s.Events) > 0 {
		d["until"] = stamp(p.until(s))
	}
	return d
}

// Summary says how far the window got, as a reason quotes it.
func (p Pattern) Summary(s *PatternState) string {
	pr := p.Progress(s)
	seen, _ := pr["seen"].(int)
	needed, _ := pr["needed"].(int)
	switch p.Kind {
	case PatternAbsence:
		return plural(len(s.Events), "event") + " of condition 1"
	case PatternCount:
		return fmt.Sprintf("%d of %d events", seen, needed)
	case PatternAll:
		return fmt.Sprintf("%d of %d conditions", seen, needed)
	case PatternSequence:
		return fmt.Sprintf("step %d of %d", seen, needed)
	}
	return ""
}

func (p Pattern) waiting(s *PatternState) string {
	until := stamp(p.until(s))
	pr := p.Progress(s)
	seen, _ := pr["seen"].(int)
	needed, _ := pr["needed"].(int)
	switch p.Kind {
	case PatternCount:
		return fmt.Sprintf("%d of %d events within the window: waiting for %d more before %s.", seen, needed, needed-seen, until)
	case PatternAll:
		have := map[int]bool{}
		for _, c := range pr["conditions"].([]int) {
			have[c] = true
		}
		var missing []string
		for c := 1; c <= p.Conditions; c++ {
			if !have[c] {
				missing = append(missing, fmt.Sprint(c))
			}
		}
		what := "condition " + missing[0]
		if len(missing) > 1 {
			what = "conditions " + strings.Join(missing[:len(missing)-1], ", ") + " and " + missing[len(missing)-1]
		}
		return fmt.Sprintf("%d of %d conditions matched: waiting for %s before %s.", seen, needed, what, until)
	case PatternSequence:
		return fmt.Sprintf("Step %d of %d reached: waiting for step %d before %s.", seen, needed, seen+1, until)
	}
	return fmt.Sprintf("%s waiting for an event of condition 2 until %s: the rule fires then if none comes.",
		capitalize(plural(len(s.Events), "event")+" of condition 1"), until)
}

// ExpiredReason is the reason of the events a window let go of.
func (p Pattern) ExpiredReason(s *PatternState) string {
	if len(s.Events) == 0 {
		return "The window ended before the pattern was complete."
	}
	return "The event left the window before the pattern was complete: " + p.Summary(s) + " remain."
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

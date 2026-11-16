package rules

import (
	"context"
	"sort"
	"time"
)

// The simulator decides events as the rule engine does, in memory: the
// filter, the key, the throttle or the pattern, and the circuit breaker,
// without the database. Backtests and `awxnext rules test` run it; the caller's Act
// stands for the guardrails and the action.

// SimRule is what the simulator needs of a rule.
type SimRule struct {
	Filter, Key, Throttle string
	Window                time.Duration
	// BreakerFirings is how many firings of a key within BreakerWindow
	// trip its circuit breaker; 0: no breaker.
	BreakerFirings int64
	BreakerWindow  time.Duration
	// Pattern is what the rule waits for on each key, over Conditions;
	// an empty Kind: none.
	Pattern    Pattern
	Conditions []string
}

// SimEvent is an event as expressions see it (Value), and when it was
// received.
type SimEvent struct {
	Value    map[string]any
	Received time.Time
}

// Member is an event the action runs for, with its key.
type Member struct {
	Index int
	Event SimEvent
	Key   string
}

// The outcomes of simulated decisions. An Act returns Fired, Approval,
// Held, Error or Failed.
const (
	NotMatched   = "not_matched"
	Fired        = "fired"
	Approval     = "awaiting_approval"
	Held         = "held"
	Throttled    = "throttled"
	Deduplicated = "deduplicated"
	Waiting      = "waiting"
	Error        = "error"
	Failed       = "failed"
)

// Outcome is what the action did, or would do, for members.
type Outcome struct {
	Name      string
	Guardrail string
	Reason    string
	Details   map[string]any
}

// Act runs the guardrails and the action for members, the first as event,
// at the time the action runs.
type Act func(members []Member, at time.Time) Outcome

// SimDecision is the simulated decision on an event.
type SimDecision struct {
	Index     int
	Outcome   string
	Key       string
	Reason    string
	Guardrail string
	Details   map[string]any
	// Miss names the part of the filter that failed.
	Miss *Miss
	// At is when the action ran: the end of its once_after window, or
	// when its pattern fired.
	At time.Time
}

// FiredOutcome says whether an outcome counts as a firing for the circuit
// breaker, as the engine counts them.
func FiredOutcome(name string) bool {
	return name == Fired || name == Approval || name == Failed
}

// Simulator decides a rule's events in the order they were received.
type Simulator struct {
	rule SimRule
	act  Act
	// Decisions are the decisions so far, by event.
	Decisions []SimDecision
	// Tripped are the keys whose breaker tripped, and when.
	Tripped map[string]time.Time
	// Firings is how many times the action ran, or asked for approval:
	// once for each once_after window and each pattern's firing.
	Firings int

	held     map[string]time.Time
	firings  map[string][]time.Time
	window   *simWindow
	patterns map[string]*PatternState
	waiting  map[int64]Member
}

type simWindow struct {
	fireAt  time.Time
	members []Member
	keys    map[string]bool
}

// NewSimulator makes a simulator of a rule.
func NewSimulator(r SimRule, act Act) *Simulator {
	return &Simulator{rule: r, act: act, Tripped: map[string]time.Time{}, held: map[string]time.Time{}, firings: map[string][]time.Time{},
		patterns: map[string]*PatternState{}, waiting: map[int64]Member{}}
}

// Add decides the next event.
func (s *Simulator) Add(ctx context.Context, ev SimEvent) {
	i := len(s.Decisions)
	s.Decisions = append(s.Decisions, SimDecision{Index: i})
	d := &s.Decisions[i]
	vars := Vars{Event: ev.Value}
	ok, miss := Filter(ctx, s.rule.Filter, vars)
	if !ok {
		d.Outcome, d.Reason, d.Miss = NotMatched, miss.Reason(), miss
		return
	}
	var matched []int
	if len(s.rule.Conditions) > 0 {
		var misses []*Miss
		if matched, misses = Conditions(ctx, s.rule.Conditions, vars); len(matched) == 0 {
			d.Outcome = NotMatched
			d.Reason, d.Details = ConditionsMiss(misses)
			return
		}
	}
	key := ""
	if s.rule.Key != "" {
		k, err := Text(ctx, s.rule.Key, vars)
		if err != nil {
			d.Outcome, d.Reason = Error, "The key could not be evaluated: "+err.Error()
			return
		}
		key = k
	}
	d.Key = key
	m := Member{Index: i, Event: ev, Key: key}
	if s.rule.Pattern.Kind != "" {
		s.advance(ev.Received)
		s.addPattern(m, matched)
		return
	}
	switch s.rule.Throttle {
	case OnceWithin:
		if until, held := s.held[key]; held && ev.Received.Before(until) {
			d.Outcome, d.Reason = Throttled, "The key acted less than the window ago."
			return
		}
		prev, had := s.held[key]
		s.held[key] = ev.Received.Add(s.rule.Window)
		s.fire([]Member{m}, ev.Received)
		if s.Decisions[i].Outcome == Held {
			// An action held back doesn't hold its key.
			if had {
				s.held[key] = prev
			} else {
				delete(s.held, key)
			}
		}
	case OnceAfter:
		if s.window != nil && !ev.Received.Before(s.window.fireAt) {
			s.fireWindow()
		}
		if s.window == nil {
			s.window = &simWindow{fireAt: ev.Received.Add(s.rule.Window), keys: map[string]bool{}}
		}
		if s.window.keys[key] {
			d.Outcome, d.Reason = Deduplicated, "The window already holds an event with this key."
			return
		}
		s.window.keys[key] = true
		s.window.members = append(s.window.members, m)
		d.Outcome, d.Reason = Waiting, "Waiting for the window to end at "+s.window.fireAt.UTC().Format(time.RFC3339)+"."
	default:
		s.fire([]Member{m}, ev.Received)
	}
}

// Finish fires the once_after window when it ends by until, and moves the
// patterns' windows to until; their events stay waiting otherwise.
func (s *Simulator) Finish(until time.Time) {
	if s.window != nil && !s.window.fireAt.After(until) {
		s.fireWindow()
	}
	s.advance(until)
}

// addPattern adds an event to its key's window, as the engine does.
func (s *Simulator) addPattern(m Member, matched []int) {
	p := s.rule.Pattern
	st := s.patterns[m.Key]
	if st == nil {
		st = &PatternState{}
		s.patterns[m.Key] = st
	}
	s.waiting[int64(m.Index)] = m
	step := p.Add(st, PatternEvent{ID: int64(m.Index), Received: m.Event.Received, Matched: matched})
	s.settle(m.Key, st, step)
	if step.Outcome != Fired {
		if step.Outcome != Waiting {
			delete(s.waiting, int64(m.Index))
		}
		s.set(m.Index, Outcome{Name: step.Outcome, Reason: step.Reason, Details: map[string]any{"pattern": p.Progress(st)}}, m.Key, time.Time{})
	}
	if len(st.Events) == 0 {
		delete(s.patterns, m.Key)
	}
}

// advance moves the windows due by now to their deadline, in order, as the
// engine's timer does.
func (s *Simulator) advance(now time.Time) {
	p := s.rule.Pattern
	for {
		key, at := "", time.Time{}
		for k, st := range s.patterns {
			d := p.Deadline(st)
			if !d.IsZero() && !d.After(now) && (key == "" || d.Before(at) || d.Equal(at) && k < key) {
				key, at = k, d
			}
		}
		if key == "" {
			return
		}
		st := s.patterns[key]
		s.settle(key, st, p.Advance(st, at))
		if len(st.Events) == 0 {
			delete(s.patterns, key)
		}
	}
}

// settle gives the events a window let go of their outcome, and fires.
func (s *Simulator) settle(key string, st *PatternState, step PatternStep) {
	p := s.rule.Pattern
	for _, e := range step.Expired {
		delete(s.waiting, e.ID)
		s.set(int(e.ID), Outcome{Name: Expired, Reason: p.ExpiredReason(st), Details: map[string]any{"pattern": p.Progress(st)}}, key, time.Time{})
	}
	for _, e := range step.Cleared {
		delete(s.waiting, e.ID)
		s.set(int(e.ID), Outcome{Name: Cleared, Reason: "An event of condition 2 came before the window ended."}, key, time.Time{})
	}
	for _, fire := range [][]PatternEvent{step.Due, step.Fire} {
		if len(fire) == 0 {
			continue
		}
		members := make([]Member, 0, len(fire))
		for _, e := range fire {
			members = append(members, s.waiting[e.ID])
			delete(s.waiting, e.ID)
		}
		s.firePattern(key, members, step.At)
	}
}

// firePattern runs the action once for a window's events, unless the key's
// breaker is open: the firing counts once.
func (s *Simulator) firePattern(key string, members []Member, at time.Time) {
	open, n := s.breaker(key, at)
	if open {
		if _, ok := s.Tripped[key]; !ok {
			s.Tripped[key] = at
		}
		for _, m := range members {
			s.set(m.Index, Outcome{Name: Held, Guardrail: "circuit_breaker", Details: map[string]any{"key": key},
				Reason: "The circuit breaker of the key is open."}, key, at)
		}
		return
	}
	out := s.act(members, at)
	s.count(out)
	for _, m := range members {
		s.set(m.Index, out, key, at)
	}
	if !FiredOutcome(out.Name) {
		return
	}
	s.firings[key] = append(s.firings[key], at)
	if s.rule.BreakerFirings > 0 && n+1 >= s.rule.BreakerFirings {
		if _, ok := s.Tripped[key]; !ok {
			s.Tripped[key] = at
		}
	}
}

func (s *Simulator) fireWindow() {
	w := s.window
	s.window = nil
	s.fire(w.members, w.fireAt)
}

// fire runs the action once for members, but for those whose key's
// breaker is open, as the engine does.
func (s *Simulator) fire(members []Member, at time.Time) {
	var acting []Member
	prior := map[string]int64{}
	for _, m := range members {
		if open, n := s.breaker(m.Key, m.Event.Received); open {
			if _, ok := s.Tripped[m.Key]; !ok {
				s.Tripped[m.Key] = m.Event.Received
			}
			s.set(m.Index, Outcome{Name: Held, Guardrail: "circuit_breaker", Details: map[string]any{"key": m.Key},
				Reason: "The circuit breaker of the key is open."}, m.Key, at)
			continue
		} else {
			prior[m.Key] = n
		}
		acting = append(acting, m)
	}
	if len(acting) == 0 {
		return
	}
	out := s.act(acting, at)
	s.count(out)
	for _, m := range acting {
		s.set(m.Index, out, m.Key, at)
		if FiredOutcome(out.Name) {
			s.firings[m.Key] = append(s.firings[m.Key], m.Event.Received)
			if s.rule.BreakerFirings > 0 && prior[m.Key]+1 >= s.rule.BreakerFirings {
				if _, ok := s.Tripped[m.Key]; !ok {
					s.Tripped[m.Key] = m.Event.Received
				}
			}
		}
	}
}

// breaker says whether a key's breaker is open for an event received at
// at, and how many times the key fired within the window before.
func (s *Simulator) breaker(key string, at time.Time) (bool, int64) {
	if s.rule.BreakerFirings <= 0 {
		return false, 0
	}
	if _, ok := s.Tripped[key]; ok {
		return true, 0
	}
	from := at.Add(-s.rule.BreakerWindow)
	times := s.firings[key]
	// Firings are kept in the order of their events, so the window's are
	// the last ones.
	start := sort.Search(len(times), func(i int) bool { return times[i].After(from) })
	var n int64
	for _, t := range times[start:] {
		if !t.After(at) {
			n++
		}
	}
	return n >= s.rule.BreakerFirings, n
}

func (s *Simulator) count(out Outcome) {
	if out.Name == Fired || out.Name == Approval {
		s.Firings++
	}
}

func (s *Simulator) set(i int, out Outcome, key string, at time.Time) {
	d := &s.Decisions[i]
	d.Outcome, d.Reason, d.Guardrail, d.Details, d.Key, d.At = out.Name, out.Reason, out.Guardrail, out.Details, key, at
}

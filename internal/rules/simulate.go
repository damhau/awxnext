package rules

import (
	"context"
	"sort"
	"time"
)

// The simulator decides events as the rule engine does, in memory: the
// filter, the key, the throttle or the pattern, the circuit breaker and
// the check of each fix by a resolving event, without the database. Backtests and `awxnext rules test` run it; the caller's Act
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
	// Check is how the rule checks its fixes by a resolving event; an
	// empty Condition: no check.
	Check SimCheck
}

// SimCheck is a rule's check by a resolving event: one matching Condition
// on the key, within Window after the action finished, the action taking
// RunTime.
type SimCheck struct {
	Condition string
	Window    time.Duration
	RunTime   time.Duration
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

// The outcomes of simulated checks.
const (
	CheckPending      = "pending"
	CheckFixed        = "fixed"
	CheckNotFixed     = "not_fixed"
	CheckActionFailed = "action_failed"
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
	// Check is what the check of the action's fix found; empty: no check.
	Check string
	// ResolvedBy is the index of the event that resolved the key.
	ResolvedBy *int
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
	checks   []*simCheck
}

type simCheck struct {
	key              string
	opened, deadline time.Time
	decisions        []int
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
	vars := Vars{Event: ev.Value}
	s.expireChecks(ev.Received, false)
	s.resolveChecks(ctx, i, ev, vars)
	d := &s.Decisions[i]
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
// patterns' windows to until; their events stay waiting otherwise. The
// checks whose window ended by until find their key not fixed; the others
// stay pending.
func (s *Simulator) Finish(until time.Time) {
	if s.window != nil && !s.window.fireAt.After(until) {
		s.fireWindow()
	}
	s.advance(until)
	s.expireChecks(until, true)
}

// openChecks opens the check of each key an action ran for.
func (s *Simulator) openChecks(members []Member, out Outcome, at time.Time) {
	if s.rule.Check.Condition == "" {
		return
	}
	var outcome string
	switch out.Name {
	case Fired, Approval:
		outcome = CheckPending
	case Failed, Error:
		outcome = CheckActionFailed
	default:
		return
	}
	byKey := map[string]*simCheck{}
	for _, m := range members {
		c := byKey[m.Key]
		if c == nil {
			c = &simCheck{key: m.Key, opened: m.Event.Received, deadline: at.Add(s.rule.Check.RunTime + s.rule.Check.Window)}
			byKey[m.Key] = c
			if outcome == CheckPending {
				s.checks = append(s.checks, c)
			} else {
				s.fixFailed(m.Key, at)
			}
		}
		if m.Event.Received.After(c.opened) {
			c.opened = m.Event.Received
		}
		c.decisions = append(c.decisions, m.Index)
		s.Decisions[m.Index].Check = outcome
	}
}

// expireChecks finds the keys of the checks whose window ended before now
// not fixed; at until too.
func (s *Simulator) expireChecks(now time.Time, until bool) {
	open := s.checks[:0]
	for _, c := range s.checks {
		if c.deadline.Before(now) || until && c.deadline.Equal(now) {
			for _, i := range c.decisions {
				s.Decisions[i].Check = CheckNotFixed
			}
			s.fixFailed(c.key, c.deadline)
			continue
		}
		open = append(open, c)
	}
	s.checks = open
}

// resolveChecks settles fixed the open checks of the key an event resolves.
func (s *Simulator) resolveChecks(ctx context.Context, i int, ev SimEvent, vars Vars) {
	if len(s.checks) == 0 {
		return
	}
	if ok, err := Match(ctx, s.rule.Check.Condition, vars); err != nil || !ok {
		return
	}
	key := ""
	if s.rule.Key != "" {
		k, err := Text(ctx, s.rule.Key, vars)
		if err != nil {
			return
		}
		key = k
	}
	open := s.checks[:0]
	for _, c := range s.checks {
		if c.key != key || ev.Received.Before(c.opened) {
			open = append(open, c)
			continue
		}
		for _, d := range c.decisions {
			s.Decisions[d].Check, s.Decisions[d].ResolvedBy = CheckFixed, &i
		}
	}
	s.checks = open
}

// fixFailed counts a fix that didn't work as one more firing of its key,
// as the engine does, and trips the key's breaker when it reaches it.
func (s *Simulator) fixFailed(key string, at time.Time) {
	if s.rule.BreakerFirings <= 0 {
		return
	}
	times := s.firings[key]
	j := sort.Search(len(times), func(i int) bool { return times[i].After(at) })
	times = append(times, time.Time{})
	copy(times[j+1:], times[j:])
	times[j] = at
	s.firings[key] = times
	if open, _ := s.breaker(key, at); open {
		if _, ok := s.Tripped[key]; !ok {
			s.Tripped[key] = at
		}
	}
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
	if FiredOutcome(out.Name) {
		s.firings[key] = append(s.firings[key], at)
		if s.rule.BreakerFirings > 0 && n+1 >= s.rule.BreakerFirings {
			if _, ok := s.Tripped[key]; !ok {
				s.Tripped[key] = at
			}
		}
	}
	s.openChecks(members, out, at)
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
	s.openChecks(acting, out, at)
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

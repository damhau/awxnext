package resources

import (
	"net/http"
	"strconv"

	"github.com/damhau/awxnext/internal/metrics"
)

// Streams and rules are labelled by id, never by key or event value, so
// that the series stay as many as the streams and rules.
var (
	streamReceived = metrics.Default.NewCounter("awxnext_event_stream_received_total",
		"Events stored by an event stream on this node.", "stream")
	streamDuplicates = metrics.Default.NewCounter("awxnext_event_stream_duplicates_total",
		"Events an event stream dropped as already received, on this node.", "stream")
	streamRefused = metrics.Default.NewCounter("awxnext_event_stream_refused_total",
		"Requests an event stream refused on this node, by reason. The stream is empty when the URL names none.", "stream", "reason")
	streamLag = metrics.Default.NewHistogram("awxnext_event_stream_lag_seconds",
		"Seconds from an event's receipt to the rules' decisions on it, on the node that decided.",
		[]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}, "stream")
	streamBacklog = metrics.Default.NewGauge("awxnext_event_stream_backlog_events",
		"Events of a stream the rules have still to decide, as of the last batch this node decided.", "stream")
	ruleMatched = metrics.Default.NewCounter("awxnext_event_rule_matched_total",
		"Events whose rule's filter matched, decided on this node.", "rule")
	ruleOutcomes = metrics.Default.NewCounter("awxnext_event_rule_decisions_total",
		"Outcomes the rules gave their decisions on this node: a decision that waits, for a window or an approval, counts again when it gets its final outcome.", "rule", "outcome")
	ruleHeld = metrics.Default.NewCounter("awxnext_event_rule_held_total",
		"Actions a guardrail held back on this node, by guardrail.", "rule", "guardrail")
	ruleChecks = metrics.Default.NewCounter("awxnext_event_rule_checks_total",
		"Checks of the rules' fixes settled on this node, by outcome: fixed, not_fixed, action_failed, error, canceled, and resolved or unresolved in shadow mode.", "rule", "outcome")
)

func idLabel(id int64) string { return strconv.FormatInt(id, 10) }

// refusal is a refused request's reason, by its status.
func refusal(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType:
		return "invalid"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "unavailable"
	}
	return "error"
}

// statusRecorder keeps the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ruleCounts are the metrics of a batch, published once it commits.
type ruleCounts struct {
	matched  map[int64]int
	outcomes map[ruleLabel]int
	held     map[ruleLabel]int
	checks   map[ruleLabel]int
}

type ruleLabel struct {
	rule  int64
	label string
}

func (a *afterCommit) matched(rule int64) {
	if a.counts.matched == nil {
		a.counts.matched = map[int64]int{}
	}
	a.counts.matched[rule]++
}

func (a *afterCommit) outcome(rule int64, out outcome, n int) {
	if a.counts.outcomes == nil {
		a.counts.outcomes, a.counts.held = map[ruleLabel]int{}, map[ruleLabel]int{}
	}
	a.counts.outcomes[ruleLabel{rule, out.name}] += n
	if out.name == "held" {
		a.counts.held[ruleLabel{rule, out.guardrail}] += n
	}
}

func (a *afterCommit) checked(rule int64, outcome string) {
	if a.counts.checks == nil {
		a.counts.checks = map[ruleLabel]int{}
	}
	a.counts.checks[ruleLabel{rule, outcome}]++
}

func (c *ruleCounts) publish() {
	for r, n := range c.matched {
		ruleMatched.With(idLabel(r)).Add(float64(n))
	}
	for l, n := range c.outcomes {
		ruleOutcomes.With(idLabel(l.rule), l.label).Add(float64(n))
	}
	for l, n := range c.held {
		ruleHeld.With(idLabel(l.rule), l.label).Add(float64(n))
	}
	for l, n := range c.checks {
		ruleChecks.With(idLabel(l.rule), l.label).Add(float64(n))
	}
}

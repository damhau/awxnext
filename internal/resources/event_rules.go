package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/rules"
)

// Event rules (awxnext, roadmap M14-02): a CEL filter over the events of
// some streams, a key and a throttle per key, and an action the rule runs
// as its owner: a launch, a notification, or an event posted to another
// stream. The evaluator is in rule_engine.go, the guardrails that hold
// actions back (roadmap M14-03) in rule_guardrails.go, and the patterns a
// rule can wait for on each key (roadmap M14-07) in rule_patterns.go.

const (
	actionLaunch = "launch"
	actionNotify = "notify"
	actionPost   = "post_event"

	// schemaEvents is how many of each stream's latest events a rule's
	// expressions are checked against.
	schemaEvents      = 100
	maxRuleStreams    = 20
	maxThrottleWindow = 7 * 24 * 3600

	modeShadow   = "shadow"
	modeApproval = "approval"
	modeAuto     = "auto"

	windowHold     = "hold"
	windowApproval = "approval"
)

// eventRule is a rule as the evaluator and the checks see it.
type eventRule struct {
	ID           int64
	Name         string
	Org          int64
	Streams      []int64
	Owner        *int64
	Filter       string
	Key          string
	Throttle     string
	Window       int64
	Action       string
	Template     *int64
	ExtraVars    map[string]string
	Limit        string
	Notification *int64
	Message      string
	Target       *int64
	EventType    string
	EventData    string

	Pattern       string
	Conditions    []string
	PatternCount  int64
	PatternWindow int64

	Mode                string
	MaxRuns             int64
	HostLock            bool
	GroupShare          int64
	BreakerFirings      int64
	BreakerWindow       int64
	BreakerNotification *int64
	WindowAction        string
	ApprovalTimeout     int64

	Check             string
	CheckCondition    string
	CheckWindow       int64
	CheckTemplate     *int64
	CheckNotification *int64
	CheckApproval     bool

	// stopped is set when the evaluator stops the rule within a batch.
	stopped bool
}

func optID(v any) *int64 {
	if id, ok := api.ID(v); ok {
		return &id
	}
	return nil
}

func streamIDs(v any) []int64 {
	var out []int64
	switch x := v.(type) {
	case []int64:
		out = append(out, x...)
	case []any:
		for _, e := range x {
			if id, ok := api.ID(e); ok {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func ruleFromObj(o Obj) *eventRule {
	r := &eventRule{
		Name: str(o["name"]), Streams: streamIDs(o["streams"]), Owner: optID(o["owner"]),
		Filter: str(o["filter"]), Key: str(o["key"]), Throttle: str(o["throttle"]), Action: str(o["action"]),
		Template: optID(o["unified_job_template"]), Limit: str(o["limit"]), Notification: optID(o["notification_template"]),
		Message: str(o["message"]), Target: optID(o["target_stream"]), EventType: str(o["event_type"]), EventData: str(o["event_data"]),
		ExtraVars: map[string]string{}, Mode: str(o["mode"]), HostLock: boolv(o["host_lock"]),
		BreakerNotification: optID(o["breaker_notification_template"]), WindowAction: str(o["window_action"]),
		Check: str(o["check"]), CheckCondition: str(o["check_condition"]), CheckTemplate: optID(o["check_template"]),
		CheckNotification: optID(o["check_notification_template"]), CheckApproval: boolv(o["check_approval"]),
	}
	r.ID, _ = api.ID(o["id"])
	r.Org, _ = api.ID(o["organization"])
	r.Window, _ = api.ID(o["throttle_window"])
	r.MaxRuns, _ = api.ID(o["max_concurrent_runs"])
	r.GroupShare, _ = api.ID(o["max_group_share"])
	r.BreakerFirings, _ = api.ID(o["breaker_firings"])
	r.BreakerWindow, _ = api.ID(o["breaker_window"])
	r.ApprovalTimeout, _ = api.ID(o["approval_timeout"])
	r.Pattern = str(o["pattern"])
	r.PatternCount, _ = api.ID(o["pattern_count"])
	r.PatternWindow, _ = api.ID(o["pattern_window"])
	r.CheckWindow, _ = api.ID(o["check_window"])
	if list, ok := o["conditions"].([]any); ok {
		for _, c := range list {
			r.Conditions = append(r.Conditions, str(c))
		}
	}
	if m, ok := o["extra_vars"].(map[string]any); ok {
		for k, v := range m {
			r.ExtraVars[k] = fmt.Sprint(v)
		}
	}
	return r
}

func validateRuleStreams(c *Ctx, v any, data Obj) error {
	var list []any
	switch x := v.(type) {
	case []any:
		list = x
	case []int64:
		for _, id := range x {
			list = append(list, id)
		}
	default:
		return errors.New("Expected a list of event stream ids.")
	}
	if len(list) == 0 {
		return errors.New("A rule reads at least one event stream.")
	}
	if len(list) > maxRuleStreams {
		return fmt.Errorf("A rule reads at most %d event streams.", maxRuleStreams)
	}
	for _, e := range list {
		if _, ok := api.ID(e); !ok {
			return fmt.Errorf("Invalid event stream id %v.", e)
		}
	}
	return nil
}

func validateRuleVars(c *Ctx, v any, data Obj) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("Expected an object: each variable's name and the CEL expression of its value.")
	}
	for k, e := range m {
		if !varNameRe.MatchString(k) {
			return fmt.Errorf("%q is not a variable name.", k)
		}
		if s, ok := e.(string); !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s: expected a CEL expression.", k)
		}
	}
	return nil
}

func validateRuleConditions(c *Ctx, v any, data Obj) error {
	list, ok := v.([]any)
	if !ok {
		return errors.New("Expected a list of CEL conditions.")
	}
	if len(list) > rules.MaxConditions {
		return fmt.Errorf("A pattern has at most %d conditions.", rules.MaxConditions)
	}
	for i, e := range list {
		s, ok := e.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("%d: expected a CEL condition.", i+1)
		}
		if len(s) > rules.MaxLength {
			return fmt.Errorf("%d: ensure this condition has no more than %d characters.", i+1, rules.MaxLength)
		}
	}
	return nil
}

func exprField(name, help string, required bool) *Field {
	return &Field{Name: name, Type: api.TText, Required: required, MaxLength: rules.MaxLength, Help: help}
}

func registerEventRules() {
	streams := api.Lookup("event_streams")
	ruleRes := api.Register(&Resource{
		Name:        "event_rules",
		Type:        "event_rule",
		Table:       "event_rules",
		Verbose:     "Event Rule",
		Ordering:    "name",
		ContentType: "eventrule",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Managed:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "streams", Type: api.TJSON, Required: true, Validate: validateRuleStreams,
				Help: "The ids of the event streams whose events the rule reads."},
			{Name: "enabled", Type: api.TBool, Default: true},
			{Name: "mode", Type: api.TChoice, Default: modeShadow, Choices: labelled(modeShadow, "Shadow", modeApproval, "Approval", modeAuto, "Auto"),
				Help: "shadow: record what the rule would do, and do nothing. approval: each action waits for a workflow approval. auto: act. Putting a rule in auto mode takes the EventRule Automate role."},
			{Name: "owner", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true,
				Help: "The user the rule runs as: whoever saved it last. Its rights are checked when it is saved and at each action."},
			exprField("filter", "A CEL condition on event, such as event.type == \"alert.firing\". The rule acts on the events it holds for.", true),
			exprField("key", "A CEL expression giving a string: events with the same key are throttled together. Empty: one key.", false),
			{Name: "throttle", Type: api.TChoice, Default: "", Choices: labelled("", "None", rules.OnceWithin, "Once within", rules.OnceAfter, "Once after"),
				Help: "once_within: act on a key's first event, then hold back its events for the window. once_after: wait for the window to end, then act once with the first event of each key."},
			{Name: "throttle_window", Type: api.TInt, Default: 0, Validate: between(0, maxThrottleWindow), Help: "The throttle's window, in seconds."},
			{Name: "pattern", Type: api.TChoice, Default: "", Choices: labelled("", "None", rules.PatternCount, "Count", rules.PatternAll, "All of",
				rules.PatternSequence, "Sequence", rules.PatternAbsence, "Absence"),
				Help: "What the rule waits for on each key, within pattern_window, before it acts once with every event that matched. count: pattern_count events. all: an event of each condition, in any order. sequence: an event of each condition, in their order. absence: an event of the first condition that no event of the second follows; the rule acts when the window ends. Empty: act on each event."},
			{Name: "conditions", Type: api.TJSON, Default: []any{}, Validate: validateRuleConditions,
				Help: "The CEL conditions of an all, sequence or absence pattern, over event: 2 to 10, 2 for an absence. An event must pass the filter as well."},
			{Name: "pattern_count", Type: api.TInt, Default: 0, Validate: between(0, rules.MaxPatternEvents), Help: "How many events a count pattern waits for."},
			{Name: "pattern_window", Type: api.TInt, Default: 0, Validate: between(0, maxThrottleWindow),
				Help: "The pattern's window, in seconds, by the time events were received."},
			{Name: "action", Type: api.TChoice, Default: actionLaunch,
				Choices: labelled(actionLaunch, "Launch a template", actionNotify, "Send a notification", actionPost, "Post an event to another stream")},
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", Nullable: true, Help: "The job or workflow template to launch."},
			{Name: "extra_vars", Type: api.TJSON, Default: Obj{}, Validate: validateRuleVars,
				Help: "The launch's variables: each name and the CEL expression of its value, over event and events."},
			{Name: "limit", Column: "job_limit", Type: api.TText, MaxLength: rules.MaxLength, Help: "A CEL expression giving the launch's limit."},
			{Name: "notification_template", Type: api.TFK, Ref: "notification_templates", Nullable: true},
			exprField("message", "A CEL expression giving the notification's message: a string, or an object a webhook posts as its body.", false),
			{Name: "target_stream", Type: api.TFK, Ref: "event_streams", Nullable: true, Help: "The stream the rule posts events to."},
			exprField("event_type", "A CEL expression giving the posted event's type.", false),
			exprField("event_data", "A CEL expression giving the posted event's data. Empty: the event's own data.", false),
			{Name: "max_concurrent_runs", Type: api.TInt, Default: 0, Validate: between(0, maxRuleRuns),
				Help: "Most jobs the rule's launches may run at once: a launch beyond is held back. 0: no limit."},
			{Name: "host_lock", Type: api.TBool, Default: false, Help: "Hold back a launch on a host that a running job uses."},
			{Name: "max_group_share", Type: api.TInt, Default: 0, Validate: between(0, 100),
				Help: "Most percent of an inventory group's hosts that the jobs of event rules may run on at once: a launch beyond is held back. 0: no limit."},
			{Name: "breaker_firings", Type: api.TInt, Default: 0, Validate: between(0, maxBreakerFirings),
				Help: "Firings of one key within breaker_window that trip its circuit breaker: the rule stops acting for the key until a person resets it. 0: no breaker."},
			{Name: "breaker_window", Type: api.TInt, Default: 3600, Validate: between(1, maxThrottleWindow), Help: "The circuit breaker's window, in seconds."},
			{Name: "breaker_notification_template", Type: api.TFK, Ref: "notification_templates", Nullable: true,
				Help: "The notification sent when a key's circuit breaker trips."},
			{Name: "window_action", Type: api.TChoice, Default: windowHold, Choices: labelled(windowHold, "Hold back", windowApproval, "Ask for approval"),
				Help: "What the rule does with an action inside a maintenance window or a change freeze: hold it back, or ask for an approval."},
			{Name: "approval_timeout", Type: api.TInt, Default: 3600, Validate: between(0, maxThrottleWindow),
				Help: "Seconds the rule's approvals wait before they time out. 0: no limit."},
			{Name: "check", Column: "check_kind", Type: api.TChoice, Default: "", Choices: labelled("", "None", checkEvent, "A resolving event", checkTemplate, "A check template"),
				Help: "How the rule checks that its action fixed the problem. event: it waits, once the action finished, for an event matching check_condition with the same key, such as the alert resolved. template: it launches check_template once the action finished, and the job's result decides. Empty: no check."},
			exprField("check_condition", "A CEL condition on event: the event that shows a key fixed, such as event.type == \"alert.resolved\". The rule's key, evaluated on it, names the key. The filter doesn't apply to it.", false),
			{Name: "check_window", Type: api.TInt, Default: 0, Validate: between(0, maxThrottleWindow),
				Help: "Seconds the check waits once the action finished: for the resolving event, or before it launches the check template."},
			{Name: "check_template", Type: api.TFK, Ref: "unified_job_templates", Nullable: true,
				Help: "The job or workflow template that checks a fix, launched with the action's variables and limit: a successful job means fixed, a failed one not fixed, unless the job sets the fixed artifact (set_stats) to true or false."},
			{Name: "check_notification_template", Type: api.TFK, Ref: "notification_templates", Nullable: true,
				Help: "The notification sent when a check finds a key not fixed, or the action failed."},
			{Name: "check_approval", Type: api.TBool, Default: false,
				Help: "Once a check finds a key not fixed, the rule's next actions for the key wait for an approval, until one fixes it. In auto mode only."},
			{Name: "success_rate", Type: api.TFloat, ReadOnly: true, Virtual: true, Nullable: true,
				Help: "The share of the rule's checked actions that fixed their key, from 0 to 1, over the last EVENT_RULE_SUCCESS_RATE_DAYS days: fixed out of fixed, not fixed and action failed. Empty: no check settled. summary_fields.checks has the counts."},
			{Name: "stopped_reason", Type: api.TText, ReadOnly: true, Help: "Why the rule stopped by itself, such as its owner losing a right."},
			{Name: "last_fired", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When the rule last acted."},
		},
		BeforeSave: saveEventRule,
		AfterCreate: func(c *Ctx, obj Obj) error {
			return ruleSaved(c, nil, obj)
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			return ruleSaved(c, old, obj)
		},
		BeforeDelete: cancelRuleApprovals,
		Summary:      ruleSummary,
		SubLists: []*SubList{readOnlyChildSub("decisions", "event_rule_decisions", "rule_id"),
			readOnlyChildSub("breakers", "event_rule_breakers", "rule_id"), readOnlyChildSub("patterns", "event_rule_patterns", "rule_id"),
			readOnlyChildSub("checks", "event_rule_checks", "rule_id")},
		Actions: []*api.Action{{Name: "reset_breaker", Methods: []string{"POST"}, Perm: "change", Handler: resetBreakerView},
			{Name: "backtest", Methods: []string{"POST"}, Perm: "view", Handler: backtestView}},
	})

	api.Register(&Resource{
		Name:         "event_rule_decisions",
		Type:         "event_rule_decision",
		Table:        "event_rule_decisions",
		Verbose:      "Event Rule Decision",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-id",
		SearchFields: []string{"event_id", "event_type", "key", "reason"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "rule", Type: api.TFK, Ref: "event_rules", ReadOnly: true},
			{Name: "stream", Type: api.TFK, Ref: "event_streams", ReadOnly: true},
			{Name: "sequence", Type: api.TInt, ReadOnly: true, Help: "The event's number in its stream."},
			{Name: "event_id", Type: api.TString, ReadOnly: true},
			{Name: "event_source", Type: api.TString, ReadOnly: true},
			{Name: "event_type", Type: api.TString, ReadOnly: true},
			{Name: "received", Type: api.TDateTime, ReadOnly: true},
			{Name: "node", Type: api.TString, ReadOnly: true, Help: "The node that decided."},
			{Name: "key", Type: api.TString, ReadOnly: true},
			{Name: "outcome", Type: api.TChoice, ReadOnly: true, Choices: labelled(
				"not_matched", "Not matched", "launched", "Launched", "notified", "Notified", "posted", "Posted", "throttled", "Throttled",
				"deduplicated", "Deduplicated", "waiting", "Waiting", "refused", "Refused", "failed", "Failed", "error", "Error", "dropped", "Dropped",
				"shadow", "Shadow", "held", "Held back", "awaiting_approval", "Awaiting approval", "denied", "Denied", "timed_out", "Timed out",
				"canceled", "Canceled", rules.Expired, "Expired", rules.Cleared, "Cleared", rules.Ignored, "Ignored")},
			{Name: "reason", Type: api.TText, ReadOnly: true},
			{Name: "mode", Type: api.TChoice, ReadOnly: true, Choices: labelled(modeShadow, "Shadow", modeApproval, "Approval", modeAuto, "Auto"),
				Help: "The rule's mode when it decided."},
			{Name: "guardrail", Type: api.TChoice, ReadOnly: true, Choices: labelled("", "None", guardBreaker, "Circuit breaker",
				guardMaintenance, "Maintenance window", guardFreeze, "Change freeze", guardRuns, "Concurrent runs", guardHostLock, "Host lock",
				guardGroupShare, "Group share", guardEscalation, "Escalated"), Help: "What held the action back, or made it wait for approval."},
			{Name: "fired", Type: api.TBool, ReadOnly: true, Help: "The action ran, asked for approval, or would have run in shadow mode: the circuit breaker counts it."},
			{Name: "approval", Column: "approval_id", Type: api.TFK, Ref: "workflow_approvals", ReadOnly: true, Nullable: true,
				Help: "The workflow approval the action waits for."},
			{Name: "window", Column: "window_id", Type: api.TInt, ReadOnly: true, Nullable: true, Help: "The once_after window the event belongs to."},
			{Name: "job", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true},
			{Name: "check", Column: "check_id", Type: api.TInt, ReadOnly: true, Nullable: true, Help: "The check of the action's fix: an event_rule_checks id."},
			{Name: "check_outcome", Type: api.TChoice, ReadOnly: true, Choices: checkOutcomeChoices(true),
				Help: "What the check of the action's fix found: pending until it settles. Empty: the action isn't checked."},
			{Name: "details", Type: api.TJSON, ReadOnly: true,
				Help: "What the decision saw: for not_matched, the filter's part that failed (filter.expression, filter.start and filter.end in the filter, filter.error) and the values it read (filter.values), or each condition's (conditions). For a rule with a pattern, its key's window (pattern: kind, seen of needed, events, until)."},
			{Name: "trace_id", Type: api.TString, ReadOnly: true, Help: "The OpenTelemetry trace of the event's decisions and of the job launched. Empty: none."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.rule_id IN (SELECT t.id FROM event_rules t WHERE %s)", api.ViewPredicate(c, ruleRes, arg))
		},
	})
	events := api.Lookup("stream_events")
	events.SubLists = append(events.SubLists, &SubList{Name: "decisions", Target: "event_rule_decisions", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string {
			// received finds the decisions' partition.
			return fmt.Sprintf(`(t.stream_id, t.sequence, t.received) = (SELECT e.stream_id, e.sequence, e.received FROM stream_events e WHERE e.id = %s)`, arg(pid))
		},
		Order: func(int64) string { return "t.rule_id" },
	})

	api.Register(&Resource{
		Name:         "event_rule_breakers",
		Type:         "event_rule_breaker",
		Table:        "event_rule_breakers",
		Verbose:      "Event Rule Breaker",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-id",
		SearchFields: []string{"key"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "rule", Type: api.TFK, Ref: "event_rules", ReadOnly: true},
			{Name: "shadow", Type: api.TBool, ReadOnly: true, Help: "Tripped by the rule in shadow mode, which only records it."},
			{Name: "key", Type: api.TString, ReadOnly: true},
			{Name: "tripped_at", Type: api.TDateTime, ReadOnly: true, Nullable: true,
				Help: "When the breaker tripped, as the event that tripped it was received. Empty: reset."},
			{Name: "firings", Type: api.TInt, ReadOnly: true, Help: "The firings within the window that tripped it."},
			{Name: "reset_at", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "reset_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.rule_id IN (SELECT t.id FROM event_rules t WHERE %s)", api.ViewPredicate(c, ruleRes, arg))
		},
	})

	api.Register(&Resource{
		Name:         "event_rule_patterns",
		Type:         "event_rule_pattern",
		Table:        "event_rule_patterns",
		Verbose:      "Event Rule Pattern",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "deadline",
		SearchFields: []string{"key"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "rule", Type: api.TFK, Ref: "event_rules", ReadOnly: true},
			{Name: "shadow", Type: api.TBool, ReadOnly: true, Help: "The window of the rule in shadow mode."},
			{Name: "key", Type: api.TString, ReadOnly: true},
			{Name: "opened", Type: api.TDateTime, ReadOnly: true, Help: "When the window's first event was received."},
			{Name: "clock", Type: api.TDateTime, ReadOnly: true, Help: "The newest time the window saw: an event older than the window by it is too late."},
			{Name: "deadline", Type: api.TDateTime, ReadOnly: true, Help: "When the window next changes with time alone: its absence ends, or its oldest event leaves it."},
			{Name: "events", Type: api.TJSON, ReadOnly: true, Help: "The events the window holds: their decision, stream, sequence and received time."},
			{Name: "progress", Type: api.TJSON, ReadOnly: true, Help: "How far the window got: kind, seen of needed, events, and until when it can complete."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.rule_id IN (SELECT t.id FROM event_rules t WHERE %s)", api.ViewPredicate(c, ruleRes, arg))
		},
	})
	registerRuleChecks(ruleRes)

	streams.Actions = append(streams.Actions, &api.Action{Name: "schema", Methods: []string{"GET"}, Perm: "view", Handler: streamSchemaView})
	streams.AfterDelete = func(c *Ctx, obj Obj) error {
		_, err := c.DB().Exec(c, `UPDATE event_rules SET streams = (SELECT coalesce(jsonb_agg(e ORDER BY e), '[]'::jsonb)
			FROM jsonb_array_elements(streams) e WHERE e <> to_jsonb($1::bigint)), modified = now()
			WHERE streams @> jsonb_build_array($1::bigint)`, obj["id"])
		return err
	}
}

// streamSchemaView serves /event_streams/<id>/schema/: the shape rules'
// expressions are checked against.
func streamSchemaView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	s, err := streamSchema(c, []int64{id})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, s.JSON())
}

// logDB is the database as the event log reads it.
func logDB(c *Ctx) eventstreams.DB {
	if db, ok := c.DB().(eventstreams.DB); ok {
		return db
	}
	return c.App.DB
}

// streamSchema infers the schema of the streams' events from their latest
// ones, test mode included.
func streamSchema(c *Ctx, streams []int64) (*rules.Schema, error) {
	rows, err := c.DB().Query(c, "SELECT DISTINCT stream_type FROM event_streams WHERE id = ANY($1)", streams)
	if err != nil {
		return nil, err
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	recent, err := eventstreams.Recent(c, logDB(c), streams, schemaEvents)
	if err != nil {
		return nil, err
	}
	vals := make([]map[string]any, len(recent))
	for i, ev := range recent {
		vals[i] = rules.Value(ev)
	}
	return rules.Infer(types, vals), nil
}

// ruleOwner is the user a rule saved now runs as: the user saving it, or,
// for a GitOps source, the user who last saved the source.
func ruleOwner(c *Ctx) (*rbac.Principal, error) {
	if !c.User.Anonymous() {
		return c.User, nil
	}
	if tag, _ := c.Extra["managed_by"].(string); strings.HasPrefix(tag, "gitops:") {
		src, _ := strconv.ParseInt(strings.TrimPrefix(tag, "gitops:"), 10, 64)
		var uid *int64
		if err := c.DB().QueryRow(c, "SELECT coalesce(modified_by_id, created_by_id) FROM gitops_sources WHERE id = $1", src).Scan(&uid); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if uid != nil {
			p, err := auth.LoadPrincipal(c, c.App, *uid)
			if err != nil || p != nil {
				return p, err
			}
		}
		return nil, api.BadRequest("Rules run as the user who last saved their GitOps source, and this source has no active one.")
	}
	return nil, api.BadRequest("Rules run as the user who saves them.")
}

func saveEventRule(c *Ctx, old, data Obj) error {
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	if v, ok := data["streams"]; ok {
		data["streams"] = streamIDs(v)
	}
	r := ruleFromObj(merged)
	enabled := boolv(merged["enabled"])
	if enabled {
		data["stopped_reason"] = ""
	}
	changed := old == nil
	for _, f := range []string{"streams", "filter", "key", "throttle", "throttle_window", "action", "unified_job_template",
		"extra_vars", "limit", "notification_template", "message", "target_stream", "event_type", "event_data",
		"mode", "max_concurrent_runs", "host_lock", "max_group_share", "breaker_notification_template",
		"pattern", "conditions", "pattern_count", "pattern_window",
		"check", "check_condition", "check_window", "check_template", "check_notification_template", "check_approval"} {
		if _, ok := data[f]; ok && (old == nil || !sameValue(old[f], data[f])) {
			changed = true
		}
	}
	if !enabled && !changed {
		return nil
	}
	if err := checkRuleShape(r); err != nil {
		return err
	}
	owner, err := ruleOwner(c)
	if err != nil {
		return err
	}
	data["owner"] = owner.UserID
	oc := *c
	oc.User = owner
	if err := checkRuleTargets(&oc, r); err != nil {
		return err
	}
	if err := checkRuleGuardrails(&oc, r); err != nil {
		return err
	}
	s, err := streamSchema(c, r.Streams)
	if err != nil {
		return err
	}
	if err := checkRuleExprs(s, r); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	reason, err := ruleRefusal(&oc, r)
	if err != nil {
		return err
	}
	if reason != "" {
		return api.Detail(403, reason)
	}
	return nil
}

// sameValue compares a stored value with a saved one: lists of ids in any
// order, numbers of any type.
func sameValue(a, b any) bool {
	norm := func(v any) any {
		switch x := v.(type) {
		case []int64:
			return streamIDs(v)
		case []any:
			if ids := streamIDs(x); len(ids) == len(x) {
				return ids
			}
		}
		return v
	}
	return fmt.Sprint(norm(a)) == fmt.Sprint(norm(b))
}

// checkRuleShape checks what a rule needs for its throttle, its pattern
// and its action.
func checkRuleShape(r *eventRule) error {
	fe := api.FieldErrors{}
	if r.Throttle != "" && r.Window < 1 {
		fe.Add("throttle_window", "A throttle needs a window of at least one second.")
	}
	checkRulePattern(r, fe)
	checkRuleCheck(r, fe)
	if r.Action != actionLaunch {
		if r.MaxRuns > 0 {
			fe.Add("max_concurrent_runs", "Only a rule that launches has runs to bound.")
		}
		if r.HostLock {
			fe.Add("host_lock", "Only a rule that launches has hosts to lock.")
		}
		if r.GroupShare > 0 {
			fe.Add("max_group_share", "Only a rule that launches has hosts to share.")
		}
	}
	switch r.Action {
	case actionLaunch:
		if r.Template == nil {
			fe.Add("unified_job_template", "A rule that launches needs a job or workflow template.")
		}
	case actionNotify:
		if r.Notification == nil {
			fe.Add("notification_template", "A rule that notifies needs a notification template.")
		}
	case actionPost:
		if r.Target == nil {
			fe.Add("target_stream", "A rule that posts events needs the stream to post them to.")
		} else if slices.Contains(r.Streams, *r.Target) {
			fe.Add("target_stream", "A rule cannot post to a stream it reads.")
		}
		if strings.TrimSpace(r.EventType) == "" {
			fe.Add("event_type", "A rule that posts events needs their type.")
		}
	}
	return fe.Err()
}

// checkRuleTargets checks the streams and the template: that they exist,
// and that the template prompts for what the rule sets.
func checkRuleTargets(c *Ctx, r *eventRule) error {
	var n int
	if err := c.DB().QueryRow(c, "SELECT count(*) FROM event_streams WHERE id = ANY($1)", r.Streams).Scan(&n); err != nil {
		return err
	}
	if n != len(r.Streams) {
		return api.FieldError("streams", "An event stream of the list does not exist.")
	}
	if err := checkCheckTemplate(c, r); err != nil {
		return err
	}
	if r.Action != actionLaunch {
		return nil
	}
	missing, noLimit, ok, err := templatePrompts(c, *r.Template, r)
	if err != nil {
		return err
	}
	if !ok {
		return api.FieldError("unified_job_template", "A rule launches job templates and workflow templates.")
	}
	fe := api.FieldErrors{}
	if len(missing) > 0 {
		fe.Add("extra_vars", fmt.Sprintf("The template does not prompt for %s: turn on its Prompt on launch for variables, or ask for them in its survey.", strings.Join(missing, ", ")))
	}
	if noLimit {
		fe.Add("limit", "The template does not prompt for a limit: turn on its Prompt on launch for the limit.")
	}
	if err := fe.Err(); err != nil {
		return err
	}
	names := map[string]any{}
	for k := range r.ExtraVars {
		names[k] = true
	}
	return refuseAnsibleVars(c, "extra_vars", names)
}

// templatePrompts says which of the rule's variables a template doesn't
// prompt for, whether it doesn't prompt for the rule's limit, and whether
// it is a job or workflow template at all.
func templatePrompts(c *Ctx, tpl int64, r *eventRule) (missing []string, noLimit, ok bool, err error) {
	var typ string
	var askVars, askLimit, survey bool
	err = c.DB().QueryRow(c, `SELECT type, ask_variables_on_launch, ask_limit_on_launch, survey_enabled FROM unified_job_templates WHERE id = $1`,
		tpl).Scan(&typ, &askVars, &askLimit, &survey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, err
	}
	if typ != "job_template" && typ != "workflow_job_template" {
		return nil, false, false, nil
	}
	if len(r.ExtraVars) > 0 && !askVars {
		known := map[string]bool{}
		if survey {
			known = surveyVariables(templateSurvey(c, tpl))
		}
		for k := range r.ExtraVars {
			if !known[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
	}
	return missing, r.Limit != "" && !askLimit, true, nil
}

// checkRuleExprs compiles and type-checks a rule's expressions against the
// schema of its streams' events.
func checkRuleExprs(s *rules.Schema, r *eventRule) error {
	fe := api.FieldErrors{}
	check := func(field, expr string, want rules.Want) {
		if err := rules.Check(s, expr, want); err != nil {
			fe.Add(field, err.Error())
		}
	}
	check("filter", r.Filter, rules.Bool)
	for i, cond := range r.Conditions {
		if err := rules.Check(s, cond, rules.Bool); err != nil {
			fe.Add("conditions", fmt.Sprintf("%d: %s", i+1, err))
		}
	}
	if r.Key != "" {
		check("key", r.Key, rules.String)
	}
	if r.Check == checkEvent {
		check("check_condition", r.CheckCondition, rules.Bool)
	}
	switch r.Action {
	case actionLaunch:
		names := make([]string, 0, len(r.ExtraVars))
		for k := range r.ExtraVars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if err := rules.Check(s, r.ExtraVars[k], rules.Any); err != nil {
				fe.Add("extra_vars", k+": "+err.Error())
			}
		}
		if r.Limit != "" {
			check("limit", r.Limit, rules.String)
		}
	case actionNotify:
		if r.Message != "" {
			check("message", r.Message, rules.Any)
		}
	case actionPost:
		check("event_type", r.EventType, rules.String)
		if r.EventData != "" {
			check("event_data", r.EventData, rules.Any)
		}
	}
	return fe.Err()
}

// ruleRefusal says why the user of c may not run the rule, or "" when they
// may: they must see its streams and be allowed its action, and, for a rule
// in auto mode, be allowed to automate it.
func ruleRefusal(c *Ctx, r *eventRule) (string, error) {
	who := c.User.Username
	if r.Mode == modeAuto {
		ok, err := api.HasPerm(c, api.Lookup("event_rules"), Obj{"id": r.ID, "organization": r.Org}, "automate")
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("%s may not put the rule in auto mode: that takes the EventRule Automate role.", who), nil
		}
	}
	streams := api.Lookup("event_streams")
	for _, s := range r.Streams {
		ok, err := api.CanView(c, streams, s)
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("%s may not view event stream %d.", who, s), nil
		}
	}
	check := func(ok bool, err error, what string) (string, error) {
		if errors.Is(err, api.ErrNotFound) {
			return fmt.Sprintf("The %s no longer exists.", what), nil
		}
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("%s may not use the %s.", who, what), nil
		}
		return "", nil
	}
	if r.Check == checkTemplate {
		if r.CheckTemplate == nil {
			return "The rule's check template was deleted.", nil
		}
		ok, err := ujtPerm(c, *r.CheckTemplate, "execute")
		if reason, err := check(ok, err, fmt.Sprintf("check template %d", *r.CheckTemplate)); reason != "" || err != nil {
			return reason, err
		}
	}
	switch r.Action {
	case actionLaunch:
		if r.Template == nil {
			return "The rule has no template to launch.", nil
		}
		ok, err := ujtPerm(c, *r.Template, "execute")
		return check(ok, err, fmt.Sprintf("template %d", *r.Template))
	case actionNotify:
		if r.Notification == nil {
			return "The rule has no notification template.", nil
		}
		ok, err := api.HasPermID(c, "notification_templates", *r.Notification, "change")
		return check(ok, err, fmt.Sprintf("notification template %d", *r.Notification))
	case actionPost:
		if r.Target == nil {
			return "The rule has no stream to post to.", nil
		}
		ok, err := api.HasPermID(c, "event_streams", *r.Target, "change")
		return check(ok, err, fmt.Sprintf("event stream %d", *r.Target))
	}
	return "", nil
}

// ruleSaved starts the rule on its streams: a stream no other enabled rule
// reads starts at its latest event, so a rule doesn't act on events
// received before it was enabled. A rule disabled, or changing mode, drops
// its open window.
func ruleSaved(c *Ctx, old, obj Obj) error {
	id, _ := api.ID(obj["id"])
	if old != nil && (str(old["check"]) != str(obj["check"]) || boolv(old["enabled"]) && !boolv(obj["enabled"])) {
		if err := cancelChecks(c, c.DB(), id, "The rule was disabled, or its check changed, before the check ended."); err != nil {
			return err
		}
	}
	if old != nil && str(old["mode"]) != str(obj["mode"]) {
		if err := dropWindows(c, c.DB(), id, "The rule changed mode before its window ended."); err != nil {
			return err
		}
		if str(obj["mode"]) == modeShadow {
			// A new shadow run starts from nothing.
			for _, sql := range []string{`DELETE FROM event_rule_keys WHERE rule_id = $1 AND shadow`,
				`DELETE FROM event_rule_breakers WHERE rule_id = $1 AND shadow`,
				`UPDATE event_rules SET shadow_since = now() WHERE id = $1`} {
				if _, err := c.DB().Exec(c, sql, id); err != nil {
					return err
				}
			}
		}
	}
	if old != nil && str(old["mode"]) == str(obj["mode"]) && patternChanged(old, obj) {
		if err := dropPatterns(c, c.DB(), id, "The rule's pattern changed before its window ended."); err != nil {
			return err
		}
	}
	if !boolv(obj["enabled"]) {
		if old != nil && boolv(old["enabled"]) {
			return dropWindows(c, c.DB(), id, "The rule was disabled before its window ended.")
		}
		return nil
	}
	if _, err := c.DB().Exec(c, `INSERT INTO event_consumer_positions (consumer, stream_id, position)
		SELECT $1, s.id, s.last_sequence FROM event_streams s
		WHERE s.id = ANY($2) AND NOT EXISTS (SELECT 1 FROM event_rules r WHERE r.enabled AND r.id <> $3 AND r.streams @> jsonb_build_array(s.id))
		ON CONFLICT (consumer, stream_id) DO UPDATE SET position = GREATEST(event_consumer_positions.position, EXCLUDED.position), modified = now()`,
		rulesConsumer, streamIDs(obj["streams"]), id); err != nil {
		return err
	}
	_, err := c.DB().Exec(c, "SELECT pg_notify($1, '')", rulesChannel)
	return err
}

// dropWindows drops a rule's once_after window and its patterns' windows:
// the decisions waiting in them are dropped.
func dropWindows(ctx context.Context, db api.DBTX, rule int64, reason string) error {
	_, err := db.Exec(ctx, `WITH w AS (DELETE FROM event_rule_windows WHERE rule_id = $1 RETURNING id)
		UPDATE event_rule_decisions SET outcome = 'dropped', reason = $2, modified = now()
		WHERE window_id IN (SELECT id FROM w) AND outcome = 'waiting'`, rule, reason)
	if err != nil {
		return err
	}
	return dropPatterns(ctx, db, rule, reason)
}

// ruleSummary names a page of rules' streams.
func ruleSummary(c *Ctx, objs []Obj) error {
	var ids []int64
	for _, o := range objs {
		ids = append(ids, streamIDs(o["streams"])...)
	}
	names := map[int64]string{}
	if len(ids) > 0 {
		rows, err := c.DB().Query(c, "SELECT id, name FROM event_streams WHERE id = ANY($1)", ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			names[id] = name
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, o := range objs {
		list := []Obj{}
		for _, id := range streamIDs(o["streams"]) {
			if n, ok := names[id]; ok {
				list = append(list, Obj{"id": id, "name": n})
			}
		}
		o["summary_fields"].(Obj)["streams"] = list
	}
	return checkSummary(c, objs)
}

// ruleJobSummary shows, on jobs a rule launched, the event that launched
// them: the rule is the event_rule summary field.
func ruleJobSummary(c *Ctx, objs []Obj) error {
	ids := make([]int64, 0, len(objs))
	for _, o := range objs {
		if id, ok := api.ID(o["id"]); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := c.DB().Query(c, "SELECT id, stream_event FROM unified_jobs WHERE id = ANY($1) AND stream_event <> '{}'::jsonb", ids)
	if err != nil {
		return err
	}
	byID := map[int64]map[string]any{}
	for rows.Next() {
		var id int64
		var ev map[string]any
		if err := rows.Scan(&id, &ev); err != nil {
			rows.Close()
			return err
		}
		byID[id] = ev
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ruled := make([]int64, 0, len(byID))
	for id := range byID {
		ruled = append(ruled, id)
	}
	checks, err := jobChecks(c, ruled)
	if err != nil {
		return err
	}
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		if ev, ok := byID[id]; ok {
			o["summary_fields"].(Obj)["stream_event"] = ev
		}
		if list, ok := checks[id]; ok {
			o["summary_fields"].(Obj)["event_rule_checks"] = list
		}
	}
	return nil
}

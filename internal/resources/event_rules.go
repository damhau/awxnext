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
// stream. The evaluator is in rule_engine.go.

const (
	actionLaunch = "launch"
	actionNotify = "notify"
	actionPost   = "post_event"

	// schemaEvents is how many of each stream's latest events a rule's
	// expressions are checked against.
	schemaEvents      = 100
	maxRuleStreams    = 20
	maxThrottleWindow = 7 * 24 * 3600
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
	Window       int
	Action       string
	Template     *int64
	ExtraVars    map[string]string
	Limit        string
	Notification *int64
	Message      string
	Target       *int64
	EventType    string
	EventData    string

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
		ExtraVars: map[string]string{},
	}
	r.ID, _ = api.ID(o["id"])
	r.Org, _ = api.ID(o["organization"])
	w, _ := api.ID(o["throttle_window"])
	r.Window = int(w)
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
			{Name: "owner", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true,
				Help: "The user the rule runs as: whoever saved it last. Its rights are checked when it is saved and at each action."},
			exprField("filter", "A CEL condition on event, such as event.type == \"alert.firing\". The rule acts on the events it holds for.", true),
			exprField("key", "A CEL expression giving a string: events with the same key are throttled together. Empty: one key.", false),
			{Name: "throttle", Type: api.TChoice, Default: "", Choices: labelled("", "None", rules.OnceWithin, "Once within", rules.OnceAfter, "Once after"),
				Help: "once_within: act on a key's first event, then hold back its events for the window. once_after: wait for the window to end, then act once with the first event of each key."},
			{Name: "throttle_window", Type: api.TInt, Default: 0, Validate: between(0, maxThrottleWindow), Help: "The throttle's window, in seconds."},
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
		Summary:  ruleSummary,
		SubLists: []*SubList{readOnlyChildSub("decisions", "event_rule_decisions", "rule_id")},
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
				"launched", "Launched", "notified", "Notified", "posted", "Posted", "throttled", "Throttled",
				"deduplicated", "Deduplicated", "waiting", "Waiting", "refused", "Refused", "failed", "Failed", "error", "Error", "dropped", "Dropped")},
			{Name: "reason", Type: api.TText, ReadOnly: true},
			{Name: "window", Column: "window_id", Type: api.TInt, ReadOnly: true, Nullable: true, Help: "The once_after window the event belongs to."},
			{Name: "job", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true},
			{Name: "details", Type: api.TJSON, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.rule_id IN (SELECT t.id FROM event_rules t WHERE %s)", api.ViewPredicate(c, ruleRes, arg))
		},
	})

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
		"extra_vars", "limit", "notification_template", "message", "target_stream", "event_type", "event_data"} {
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
		switch v.(type) {
		case []any, []int64:
			return streamIDs(v)
		}
		return v
	}
	return fmt.Sprint(norm(a)) == fmt.Sprint(norm(b))
}

// checkRuleShape checks what a rule needs for its throttle and action.
func checkRuleShape(r *eventRule) error {
	fe := api.FieldErrors{}
	if r.Throttle != "" && r.Window < 1 {
		fe.Add("throttle_window", "A throttle needs a window of at least one second.")
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
	if r.Action != actionLaunch {
		return nil
	}
	var typ string
	var askVars, askLimit, survey bool
	err := c.DB().QueryRow(c, `SELECT type, ask_variables_on_launch, ask_limit_on_launch, survey_enabled FROM unified_job_templates WHERE id = $1`,
		*r.Template).Scan(&typ, &askVars, &askLimit, &survey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if typ != "job_template" && typ != "workflow_job_template" {
		return api.FieldError("unified_job_template", "A rule launches job templates and workflow templates.")
	}
	fe := api.FieldErrors{}
	if len(r.ExtraVars) > 0 && !askVars {
		known := map[string]bool{}
		if survey {
			known = surveyVariables(templateSurvey(c, *r.Template))
		}
		var missing []string
		for k := range r.ExtraVars {
			if !known[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			fe.Add("extra_vars", fmt.Sprintf("The template does not prompt for %s: turn on its Prompt on launch for variables, or ask for them in its survey.", strings.Join(missing, ", ")))
		}
	}
	if r.Limit != "" && !askLimit {
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
	if r.Key != "" {
		check("key", r.Key, rules.String)
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
// may: they must see its streams and be allowed its action.
func ruleRefusal(c *Ctx, r *eventRule) (string, error) {
	who := c.User.Username
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
// received before it was enabled. A rule disabled drops its open window.
func ruleSaved(c *Ctx, old, obj Obj) error {
	id, _ := api.ID(obj["id"])
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

func dropWindows(ctx context.Context, db api.DBTX, rule int64, reason string) error {
	_, err := db.Exec(ctx, `WITH w AS (DELETE FROM event_rule_windows WHERE rule_id = $1 RETURNING id)
		UPDATE event_rule_decisions SET outcome = 'dropped', reason = $2, modified = now()
		WHERE window_id IN (SELECT id FROM w) AND outcome = 'waiting'`, rule, reason)
	return err
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
	return nil
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
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		if ev, ok := byID[id]; ok {
			o["summary_fields"].(Obj)["stream_event"] = ev
		}
	}
	return nil
}

package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rules"
)

// The rule editor's check (roadmap M14-05): a draft's expressions,
// type-checked against its streams' schema as a save checks them, then run
// on the streams' latest events, test mode's included, so that a rule is
// written against the events it will read. It records nothing and acts on
// nothing: guardrails and throttles are the backtest's.

const (
	checkEvents    = 20
	maxCheckEvents = 100
	checkTimeout   = 10 * time.Second
)

// ruleDraft is what the check reads of a draft rule.
type ruleDraft struct {
	streams                                        []int64
	action, filter, key, limit, message, typ, data string
	vars                                           map[string]string
	events                                         int
}

// checkDraft serves POST /event_rules/check/.
func checkDraft(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	d, err := readRuleDraft(body)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	streams := api.Lookup("event_streams")
	for _, id := range d.streams {
		if _, err := api.Get(c, streams, id); errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, api.FieldError("streams", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id)))
			return
		} else if err != nil {
			api.WriteError(w, err)
			return
		}
		ok, err := api.CanView(c, streams, id)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		if !ok {
			api.WriteError(w, api.Detail(403, fmt.Sprintf("You may not see event stream %d, which the check reads.", id)))
			return
		}
	}
	res, err := checkRuleDraft(c, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, res)
}

func readRuleDraft(body Obj) (*ruleDraft, error) {
	fe := api.FieldErrors{}
	if err := validateRuleStreams(nil, body["streams"], body); err != nil {
		fe.Add("streams", err.Error())
	}
	d := &ruleDraft{streams: streamIDs(body["streams"]), action: str(body["action"]), filter: str(body["filter"]), key: str(body["key"]),
		limit: str(body["limit"]), message: str(body["message"]), typ: str(body["event_type"]), data: str(body["event_data"]),
		vars: map[string]string{}, events: checkEvents}
	if d.action == "" {
		d.action = actionLaunch
	}
	if d.action != actionLaunch && d.action != actionNotify && d.action != actionPost {
		fe.Add("action", fmt.Sprintf("\"%s\" is not a valid choice.", d.action))
	}
	if v, ok := body["extra_vars"]; ok && v != nil {
		if err := validateRuleVarNames(v); err != nil {
			fe.Add("extra_vars", err.Error())
		} else {
			for k, e := range v.(map[string]any) {
				d.vars[k] = str(e)
			}
		}
	}
	if v, ok := body["events"]; ok {
		n, isNum := api.ID(v)
		if !isNum || n < 1 || n > maxCheckEvents {
			fe.Add("events", fmt.Sprintf("Expected a whole number from 1 to %d.", maxCheckEvents))
		} else {
			d.events = int(n)
		}
	}
	return d, fe.Err()
}

// validateRuleVarNames checks a draft's variables: names, and expressions
// that may still be empty while they are written.
func validateRuleVarNames(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("Expected an object: each variable's name and the CEL expression of its value.")
	}
	for k, e := range m {
		if !varNameRe.MatchString(k) {
			return fmt.Errorf("%q is not a variable name.", k)
		}
		if _, ok := e.(string); !ok {
			return fmt.Errorf("%s: expected a CEL expression.", k)
		}
	}
	return nil
}

// draftExpr is one of a draft's expressions: its field, as the check's
// errors name it, and what it must give.
type draftExpr struct {
	field, expr string
	want        rules.Want
}

func (d *ruleDraft) mappings() []draftExpr {
	var out []draftExpr
	switch d.action {
	case actionLaunch:
		names := make([]string, 0, len(d.vars))
		for k := range d.vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out = append(out, draftExpr{"extra_vars." + k, d.vars[k], rules.Any})
		}
		out = append(out, draftExpr{"limit", d.limit, rules.String})
	case actionNotify:
		out = append(out, draftExpr{"message", d.message, rules.Any})
	case actionPost:
		out = append(out, draftExpr{"event_type", d.typ, rules.String}, draftExpr{"event_data", d.data, rules.Any})
	}
	return out
}

func checkRuleDraft(c *Ctx, d *ruleDraft) (Obj, error) {
	ctx, cancel := context.WithTimeout(c, checkTimeout)
	defer cancel()
	s, err := streamSchema(c, d.streams)
	if err != nil {
		return nil, err
	}
	errs := Obj{}
	ok := func(e draftExpr) bool {
		if strings.TrimSpace(e.expr) == "" {
			return false
		}
		if err := rules.Check(s, e.expr, e.want); err != nil {
			errs[e.field] = err.Error()
			return false
		}
		return true
	}
	filter := ok(draftExpr{"filter", d.filter, rules.Bool})
	key := ok(draftExpr{"key", d.key, rules.String})
	var mapped []draftExpr
	for _, m := range d.mappings() {
		if ok(m) {
			mapped = append(mapped, m)
		}
	}
	recent, err := eventstreams.Recent(ctx, logDB(c), d.streams, d.events)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].Received.After(recent[j].Received) })
	recent = recent[:min(len(recent), d.events)]
	matched := 0
	events := make([]Obj, 0, len(recent))
	for _, ev := range recent {
		o := Obj{"stream": ev.Stream, "sequence": ev.Sequence, "event_id": ev.ID, "event_type": ev.Type, "source": ev.Source,
			"subject": ev.Subject, "received": api.FormatTime(ev.Received), "test_mode": ev.TestMode}
		events = append(events, o)
		if !filter || ctx.Err() != nil {
			continue
		}
		vars := rules.Vars{Event: rules.Value(ev)}
		hit, miss := rules.Filter(ctx, d.filter, vars)
		o["matched"] = hit
		if !hit {
			o["reason"], o["filter"] = miss.String(), miss.Details()
			continue
		}
		matched++
		if key {
			if k, err := rules.Text(ctx, d.key, vars); err != nil {
				o["key_error"] = err.Error()
			} else {
				o["key"] = k
			}
		}
		values, failed := Obj{}, Obj{}
		for _, m := range mapped {
			var v any
			var err error
			if m.want == rules.String {
				v, err = rules.Text(ctx, m.expr, vars)
			} else {
				v, err = rules.Eval(ctx, m.expr, vars)
			}
			if err != nil {
				failed[m.field] = err.Error()
				continue
			}
			if name, isVar := strings.CutPrefix(m.field, "extra_vars."); isVar {
				vs, _ := values["extra_vars"].(Obj)
				if vs == nil {
					vs = Obj{}
					values["extra_vars"] = vs
				}
				vs[name] = v
			} else {
				values[m.field] = v
			}
		}
		o["action"] = values
		if len(failed) > 0 {
			o["action_errors"] = failed
		}
	}
	return Obj{"schema_events": s.Events, "errors": errs, "checked": len(events), "matched": matched,
		"truncated": ctx.Err() != nil, "events": events}, nil
}

package rulebooks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/rules"
)

func (t *translation) actions(rc *rsCtx, r rule, rr *RuleReport, sh *shape) {
	base := Rule{Ruleset: rc.rs.Name, Rule: r.Name, Streams: rc.streams, Filter: sh.filter, Pattern: sh.pattern,
		Conditions: sh.conditions, PatternCount: sh.patternCount, EventKey: sh.key}
	if sh.pattern != "" {
		base.PatternWindow = sh.window
	}
	if sh.throttle != nil {
		base.Throttle, base.ThrottleWindow, base.EventKey = sh.throttle.kind, sh.throttle.window, sh.throttle.key
	}
	var made []Rule
	var logs []int
	for i, a := range r.Actions {
		ar := rr.Actions[i]
		if a.err != nil {
			ar.Reason = "The action can't be read: " + a.err.Error() + "."
			continue
		}
		spec := base
		spec.Action = i
		spec.Key = fmt.Sprintf("rule:%s/%s/%d", rc.rs.Name, r.Name, i)
		var err error
		switch a.Kind {
		case "run_job_template", "run_workflow_template":
			err = t.launch(rc, r, a, sh, &spec, rr)
		case "post_event", "set_fact":
			err = t.post(rc, a, sh, &spec, rr)
		case "debug", "print_event", "none":
			logs = append(logs, i)
			continue
		case "retract_fact":
			err = refuse("awxnext keeps no facts to retract; set_fact posts an event")
		case "shutdown":
			err = refuse("an awxnext rule runs until it is disabled: shutdown has nothing to stop")
		case "run_playbook", "run_module":
			err = refuse("awxnext runs playbooks and modules through templates, not from rules. Launch a job template that runs it with run_job_template")
		default:
			err = refuse("ansible-rulebook 1.3 has no such action")
		}
		if err == nil {
			err = spec.check()
		}
		if err != nil {
			ar.Reason = reason(err)
			continue
		}
		ar.Status, ar.As = Translated, spec.Kind
		made = append(made, spec)
	}
	if len(made) == 0 && len(logs) > 0 {
		i := logs[0]
		spec := base
		spec.Action, spec.Kind = i, ActionLog
		spec.Key = fmt.Sprintf("rule:%s/%s/%d", rc.rs.Name, r.Name, i)
		a := r.Actions[i]
		msg, err := t.message(rc, a, sh)
		if err != nil {
			rr.Notes = append(rr.Notes, "The decision log doesn't record the message: "+clause(err))
		}
		spec.Message = msg
		if err := spec.check(); err != nil {
			rr.Actions[i].Reason = reason(err)
		} else {
			rr.Actions[i].Status, rr.Actions[i].As = Translated, ActionLog
			made = append(made, spec)
		}
		logs = logs[1:]
	}
	for _, i := range logs {
		if len(made) == 0 {
			rr.Actions[i].Reason = "The rule isn't translated."
			continue
		}
		rr.Actions[i].Status, rr.Actions[i].As = Translated, "decisions"
	}
	if len(made) > 1 {
		rr.Notes = append(rr.Notes, "Each of the rule's actions is a rule of its own: they act on the same events, without waiting for each other.")
	}
	for _, spec := range made {
		parts := []string{rc.rs.Name, r.Name}
		if len(made) > 1 {
			parts = append(parts, fmt.Sprintf("action %d", spec.Action+1))
		}
		spec.Name = t.name(parts...)
		spec.Description = fmt.Sprintf("Imported from rulebook %s: ruleset %s, rule %s.", t.o.Name, rc.rs.Name, r.Name)
		t.plan.Rules = append(t.plan.Rules, spec)
	}
}

// check compiles the rule's expressions: a translation awxnext can't run
// is no translation.
func (r *Rule) check() error {
	exprs := map[string]string{"filter": r.Filter, "key": r.EventKey, "limit": r.Limit, "event_data": r.EventData, "message": r.Message}
	if r.EDAVars != "" {
		exprs["ansible_eda"] = r.EDAVars
	}
	for k, v := range r.ExtraVars {
		exprs["extra_vars."+k] = v
	}
	for i, c := range r.Conditions {
		exprs[fmt.Sprintf("condition %d", i+1)] = c
	}
	names := make([]string, 0, len(exprs))
	for k := range exprs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		e := exprs[k]
		if e == "" {
			continue
		}
		if len(e) > rules.MaxLength {
			return refuse("its %s in CEL is longer than awxnext's %d characters", k, rules.MaxLength)
		}
		if _, err := rules.Compile(e); err != nil {
			return refuse("awxnext can't compile its %s (%s): %v", k, e, err)
		}
	}
	return nil
}

var launchArgs = map[string]bool{"name": true, "organization": true, "job_args": true, "include_events": true, "var_root": true}

func (t *translation) launch(rc *rsCtx, r rule, a action, sh *shape, spec *Rule, rr *RuleReport) error {
	var name, org any
	for _, arg := range []struct {
		key  string
		into *any
	}{{"name", &name}, {"organization", &org}} {
		what := "the template's " + arg.key
		v, ok := a.Args[arg.key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return refuse("%s is missing", what)
		}
		// awxnext looks the template up when it imports the rulebook: its
		// name can come from the rulebook's variables, not from the event.
		for _, m := range jinjaVar.FindAllStringSubmatch(v, -1) {
			if root, _, _ := strings.Cut(m[1], "."); root == "event" || root == "events" || root == "fact" || root == "facts" {
				return refuse("%s comes from the event: awxnext looks the template up when it imports the rulebook", what)
			}
		}
		r, err := renderValue(v, t.o.Variables)
		if err != nil {
			return refuse("%s: %s awxnext looks the template up when it imports the rulebook", what, clause(err))
		}
		*arg.into = r
	}
	typ := "job_template"
	if a.Kind == "run_workflow_template" {
		typ = "workflow_job_template"
	}
	spec.Kind = ActionLaunch
	spec.Template = &Template{Name: str(name), Organization: str(org), Type: typ}
	vars := t.o.Variables
	keys := make([]string, 0, len(a.Args))
	for k := range a.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if launchArgs[k] {
			continue
		}
		switch k {
		case "post_events", "set_facts", "ruleset", "retry", "retries", "delay":
		case "labels", "add_event_uuid_label":
			rr.Reasons = append(rr.Reasons, fmt.Sprintf("%s: %s isn't translated: awxnext's rules don't label their jobs.", a.Kind, k))
		case "lock":
			rr.Reasons = append(rr.Reasons, fmt.Sprintf("%s: lock isn't translated: host_lock or max_concurrent_runs bound a rule's jobs.", a.Kind))
		default:
			rr.Reasons = append(rr.Reasons, fmt.Sprintf("%s: the argument %s isn't translated.", a.Kind, k))
		}
	}
	jobArgs, _ := a.Args["job_args"].(map[string]any)
	if a.Args["job_args"] != nil && jobArgs == nil {
		return refuse("job_args is not a mapping")
	}
	spec.ExtraVars = map[string]string{}
	jk := make([]string, 0, len(jobArgs))
	for k := range jobArgs {
		jk = append(jk, k)
	}
	sort.Strings(jk)
	for _, k := range jk {
		v := jobArgs[k]
		switch k {
		case "extra_vars":
			ev, ok := v.(map[string]any)
			if !ok && v != nil {
				return refuse("job_args.extra_vars is not a mapping")
			}
			for _, name := range sortedKeys(ev) {
				val := ev[name]
				if !varName.MatchString(name) {
					return refuse("job_args.extra_vars: %q is not a variable name", name)
				}
				s, err := rc.actValue(val, vars, sh.conds, sh.names)
				if err != nil {
					return refuse("job_args.extra_vars.%s: %s", name, strings.TrimSuffix(clause(err), "."))
				}
				spec.ExtraVars[name] = s
			}
		case "limit":
			var s string
			var err error
			switch l := v.(type) {
			case []any:
				if s, err = rc.actValue(l, vars, sh.conds, sh.names); err == nil {
					s = fmt.Sprintf("%s.map(h, string(h)).join(\",\")", s)
				}
			default:
				if s, err = rc.actValue(v, vars, sh.conds, sh.names); err == nil {
					s = fmt.Sprintf("string(%s)", s)
				}
			}
			if err != nil {
				return refuse("job_args.limit: %s", strings.TrimSuffix(clause(err), "."))
			}
			spec.Limit, spec.LimitSet = s, true
		default:
			rr.Reasons = append(rr.Reasons, fmt.Sprintf("%s: job_args.%s isn't translated: awxnext's rules launch with variables and a limit.", a.Kind, k))
		}
	}
	if !spec.LimitSet {
		if sh.conds > 0 {
			spec.Limit = celString(rc.hosts)
			rr.Notes = append(rr.Notes, fmt.Sprintf("The launch's limit is the ruleset's hosts (%s): ansible-rulebook takes the hosts of the events, when they have some.", rc.hosts))
		} else {
			bs := rc.branches()
			spec.Limit = bs[0].v.limit(rc.hosts)
			if len(bs) > 1 {
				if other := bs[1].v.limit(rc.hosts); other != spec.Limit {
					spec.Limit = fmt.Sprintf("(%s ? %s : %s)", bs[0].guard("event"), spec.Limit, other)
				}
			}
		}
	}
	if err := t.followUp(rc, a, spec, rr); err != nil {
		return err
	}
	include := true
	if b, ok := a.Args["include_events"].(bool); ok {
		include = b
	}
	if include {
		eda, err := t.edaVars(rc, r, a, sh)
		if err != nil {
			return err
		}
		spec.EDAVars = eda
	}
	return nil
}

// followUp translates what ansible-rulebook does once the job ends:
// launch it again when it failed, and post its artifacts.
func (t *translation) followUp(rc *rsCtx, a action, spec *Rule, rr *RuleReport) error {
	retries, err := count(a.Args["retries"], "retries", MaxRetries)
	if err != nil {
		return err
	}
	if b, _ := a.Args["retry"].(bool); b {
		retries = max(retries, 1)
	}
	delay, err := count(a.Args["delay"], "delay", MaxRetryDelay)
	if err != nil {
		return err
	}
	if retries > 0 {
		spec.Retries, spec.RetryDelay = retries, delay
	}
	post, _ := a.Args["post_events"].(bool)
	facts, _ := a.Args["set_facts"].(bool)
	if !post && !facts {
		return nil
	}
	target := rc
	if name := str(a.Args["ruleset"]); name != "" {
		if target = t.sets[name]; target == nil {
			return refuse("the ruleset %s is not in this rulebook: awxnext posts the artifacts to the streams of the rulebook's rulesets", name)
		}
	}
	spec.PostArtifacts, spec.ArtifactsStream = "event", target.main
	if facts {
		spec.PostArtifacts = "fact"
		rr.Notes = append(rr.Notes, "set_facts posts the job's artifacts as an event: awxnext keeps no facts, so they match the rules once, as they come.")
	}
	if post && facts {
		rr.Notes = append(rr.Notes, "post_events and set_facts both post the artifacts: awxnext posts them once.")
	}
	return nil
}

// count reads a whole number of an action's arguments.
func count(v any, name string, limit int64) (int64, error) {
	switch n := v.(type) {
	case nil:
		return 0, nil
	case int64:
		if n < 0 {
			return 0, nil
		}
		if n > limit {
			return 0, refuse("%s: awxnext takes %d at most", name, limit)
		}
		return n, nil
	case float64:
		if n > float64(limit) {
			return 0, refuse("%s: awxnext takes %d at most", name, limit)
		}
		return int64(max(0, n+0.999999)), nil
	}
	return 0, refuse("%s is not a number", name)
}

// edaVars is the ansible_eda variable: the ruleset, the rule, and the
// event or events, as the playbook gets them under ansible-rulebook.
func (t *translation) edaVars(rc *rsCtx, r rule, a action, sh *shape) (string, error) {
	head := fmt.Sprintf(`"ruleset": %s, "rule": %s`, celString(rc.rs.Name), celString(r.Name))
	whole := func(base string) string {
		bs := rc.branches()
		w := bs[0].v.on(base).whole()
		if len(bs) > 1 {
			if other := bs[1].v.on(base).whole(); other != w {
				w = fmt.Sprintf("(%s ? %s : %s)", bs[0].guard(base), w, other)
			}
		}
		return w
	}
	if sh.conds > 0 {
		if a.Args["var_root"] != nil {
			return "", refuse("var_root on a rule with several conditions")
		}
		names := make([]string, sh.conds)
		for i := range names {
			names[i] = fmt.Sprintf("m_%d", i)
		}
		for n, i := range sh.names {
			names[i] = n
		}
		parts := make([]string, sh.conds)
		for i := range parts {
			parts[i] = fmt.Sprintf(`%s: (events.size() > %d ? %s : {})`, celString(names[i]), i, whole(fmt.Sprintf("events[%d]", i)))
		}
		return fmt.Sprintf(`{%s, "events": {%s}}`, head, strings.Join(parts, ", ")), nil
	}
	ev := whole("event")
	if vr := a.Args["var_root"]; vr != nil {
		root, ok := vr.(string)
		if !ok {
			return "", refuse("a var_root that is not a string")
		}
		n, err := parseCondition("event." + root)
		p, isPath := n.(*pathNode)
		if err != nil || !isPath {
			return "", refuse("the var_root %q is not an attribute path", root)
		}
		var sub ref
		bs := rc.branches()
		if sub, err = bs[len(bs)-1].v.resolve(p.parts); err != nil {
			return "", err
		}
		if !sub.absent {
			ev = fmt.Sprintf("(%s ? %s : %s)", sub.guardExpr(), sub.value(), ev)
		}
	}
	return fmt.Sprintf(`{%s, "event": %s}`, head, ev), nil
}

func (t *translation) post(rc *rsCtx, a action, sh *shape, spec *Rule, rr *RuleReport) error {
	field := "event"
	if a.Kind == "set_fact" {
		field = "fact"
	}
	target := rc
	if name := str(a.Args["ruleset"]); name != "" {
		if target = t.sets[name]; target == nil {
			return refuse("the ruleset %s is not in this rulebook: awxnext posts to the streams of the rulebook's rulesets", name)
		}
	}
	ev, ok := a.Args[field].(map[string]any)
	if !ok {
		return refuse("%s needs its %s, a mapping", a.Kind, field)
	}
	data, err := rc.actValue(ev, t.o.Variables, sh.conds, sh.names)
	if err != nil {
		return refuse("its %s: %s", field, strings.TrimSuffix(clause(err), "."))
	}
	spec.Kind, spec.TargetStream, spec.EventType, spec.EventData = ActionPost, target.main, celString(a.Kind), data
	if a.Kind == "set_fact" {
		rr.Notes = append(rr.Notes, "set_fact posts the fact as an event: awxnext keeps no facts, so a fact matches the rules once, as it comes, and no later event's conditions.")
	}
	for _, k := range sortedKeys(a.Args) {
		switch k {
		case field, "ruleset":
		default:
			rr.Reasons = append(rr.Reasons, fmt.Sprintf("%s: the argument %s isn't translated.", a.Kind, k))
		}
	}
	return nil
}

// message is what a log rule records of a debug or print_event.
func (t *translation) message(rc *rsCtx, a action, sh *shape) (string, error) {
	switch a.Kind {
	case "debug":
		if msg, ok := a.Args["msg"]; ok {
			return rc.actValue(msg, t.o.Variables, sh.conds, sh.names)
		}
		if v, ok := a.Args["var"].(string); ok {
			return rc.actValue("{{ "+v+" }}", t.o.Variables, sh.conds, sh.names)
		}
	case "print_event":
		if sh.conds > 0 {
			return "events.map(e, e.data)", nil
		}
		if vr, ok := a.Args["var_root"].(string); ok {
			s, err := rc.actValue("{{ event."+vr+" }}", t.o.Variables, 0, nil)
			if err == nil {
				return s, nil
			}
		}
		return rc.actValue("{{ event }}", t.o.Variables, 0, nil)
	}
	return "", nil
}

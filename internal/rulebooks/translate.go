package rulebooks

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/damhau/awxnext/internal/rules"
)

// The statuses of the report's sources, rules and actions.
const (
	Translated    = "translated"
	Partial       = "partial"
	NotTranslated = "not_translated"
)

// The actions of the rules a translation makes.
const (
	ActionLaunch = "launch"
	ActionPost   = "post_event"
	ActionLog    = "log"
)

// The stream types a translation makes.
const (
	StreamAlertmanager = "alertmanager"
	StreamGeneric      = "generic"
	StreamCloudEvents  = "cloudevents"
)

// DefaultEventsTTL is how long ansible-rulebook keeps the events of a
// rule's partial match, unless the ruleset's default_events_ttl or the
// rule's timeout says otherwise: an all pattern's window.
const DefaultEventsTTL = 2 * 3600

// maxWindow is the longest window a rule takes.
const maxWindow = 7 * 24 * 3600

// Options are what a translation needs beside the YAML.
type Options struct {
	// Name starts the names of the objects: the rulebook's.
	Name string
	// Variables are the rulebook's variables: vars.x in conditions, and
	// {{ x }} in the arguments of sources and actions.
	Variables map[string]any
	// SourceStreams are the sources fed by an existing event stream
	// instead, as EDA's source mappings: by the source's SourceName, the
	// stream's id. Their events reach the ruleset as a webhook's do.
	SourceStreams map[string]int64
}

// Plan is what a rulebook becomes. Each object has a key, stable across
// imports, that ties it to the piece of the rulebook it comes from.
type Plan struct {
	Streams    []Stream
	Connectors []Connector
	Rules      []Rule
	Report     *Report
}

// Stream is an event stream: one per webhook or Alertmanager source, and
// one per ruleset for its connectors and the events its rules post.
type Stream struct {
	Key, Name, Type, Ruleset string
	// Existing is the id of the stream a source mapping names: the
	// import uses it rather than making a stream.
	Existing int64
}

// Connector runs a source plugin, posting to the stream named by Stream,
// a stream's key.
type Connector struct {
	Key, Name, Stream, Source string
	Args                      map[string]any
	Filters                   []any
}

// Template is the template a launch names.
type Template struct {
	Name, Organization string
	// Type is job_template or workflow_job_template.
	Type string
}

// Rule is an event rule, for one action of a rulebook's rule. Its
// expressions are CEL.
type Rule struct {
	Key, Name, Description string
	// Ruleset, Rule and Action locate it in the report.
	Ruleset, Rule              string
	Action                     int
	Kind                       string
	Streams                    []string
	Filter, EventKey, Throttle string
	ThrottleWindow             int64
	Pattern                    string
	Conditions                 []string
	PatternCount               int64
	PatternWindow              int64

	Template *Template
	// ExtraVars are the action's job_args.extra_vars; EDAVars the
	// ansible_eda variable ansible-rulebook adds, empty when
	// include_events is false.
	ExtraVars map[string]string
	EDAVars   string
	Limit     string
	// LimitSet: the action sets the limit, rather than ansible-rulebook
	// taking the event's hosts or the ruleset's.
	LimitSet bool

	TargetStream string
	EventType    string
	EventData    string

	// Message is what a log rule records: debug's msg, or the event
	// print_event prints.
	Message string

	// Retries and RetryDelay launch a failed job again; PostArtifacts
	// (event or fact) posts the job's artifacts to ArtifactsStream.
	Retries, RetryDelay int64
	PostArtifacts       string
	ArtifactsStream     string
}

// The bounds of a launch's retries, and of the delay before each, in
// seconds.
const (
	MaxRetries    = 10
	MaxRetryDelay = 3600
)

// Report says what became of each piece of the rulebook.
type Report struct {
	Rulesets []*RulesetReport `json:"rulesets"`
	Summary  Summary          `json:"summary"`
}

// Summary counts the rules by status.
type Summary struct {
	Rules         int `json:"rules"`
	Translated    int `json:"translated"`
	Partial       int `json:"partial"`
	NotTranslated int `json:"not_translated"`
}

// RulesetReport is a ruleset's part of the report.
type RulesetReport struct {
	Name    string          `json:"name"`
	Sources []*SourceReport `json:"sources"`
	Rules   []*RuleReport   `json:"rules"`
	Notes   []string        `json:"notes,omitempty"`
	Streams []Object        `json:"streams,omitempty"`
}

// SourceReport is a source's.
type SourceReport struct {
	Name    string   `json:"name"`
	Plugin  string   `json:"plugin"`
	Status  string   `json:"status"`
	As      string   `json:"as,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Object  *Object  `json:"object,omitempty"`
	// Key is the plan's stream or connector the source became.
	Key string `json:"-"`
}

// RuleReport is a rule's: translated when each of its actions is,
// partial when some are, not translated when none is, each with its
// reasons. Notes are where the translation behaves otherwise than
// ansible-rulebook.
type RuleReport struct {
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Actions []*ActionReport `json:"actions"`
	Reasons []string        `json:"reasons,omitempty"`
	Notes   []string        `json:"notes,omitempty"`
}

// ActionReport is an action's. As says what it became: a rule's action
// (launch, post_event, log), or "decisions" for a debug or print_event
// that the decisions of the rule's other actions record.
type ActionReport struct {
	Action string  `json:"action"`
	Status string  `json:"status"`
	As     string  `json:"as,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Object *Object `json:"object,omitempty"`
}

// Object is an object an import made.
type Object struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Finish sets the rules' statuses from their actions', and the summary.
func (r *Report) Finish() {
	r.Summary = Summary{}
	for _, rs := range r.Rulesets {
		for _, rr := range rs.Rules {
			done, missed := 0, 0
			for _, a := range rr.Actions {
				if a.Status == Translated {
					done++
				} else {
					missed++
				}
			}
			switch {
			case done == 0:
				rr.Status = NotTranslated
			case missed > 0 || len(rr.Reasons) > 0:
				rr.Status = Partial
			default:
				rr.Status = Translated
			}
			r.Summary.Rules++
			switch rr.Status {
			case Translated:
				r.Summary.Translated++
			case Partial:
				r.Summary.Partial++
			default:
				r.Summary.NotTranslated++
			}
		}
	}
}

// Lookup finds the report of a rule's action.
func (r *Report) Lookup(ruleset, rule string, action int) (*RuleReport, *ActionReport) {
	for _, rs := range r.Rulesets {
		if rs.Name != ruleset {
			continue
		}
		for _, rr := range rs.Rules {
			if rr.Name == rule && action < len(rr.Actions) {
				return rr, rr.Actions[action]
			}
		}
	}
	return nil, nil
}

// The plugins that become streams, and the plugins ansible-rulebook runs
// from itself whose ansible.eda twins a connector runs.
var (
	webhookPlugins      = map[string]bool{"ansible.eda.webhook": true, "eda.builtin.webhook": true}
	alertmanagerPlugins = map[string]bool{"ansible.eda.alertmanager": true}
	builtinSources      = map[string]string{"eda.builtin.generic": "ansible.eda.generic", "eda.builtin.range": "ansible.eda.range",
		"eda.builtin.pg_listener": "ansible.eda.pg_listener"}
	builtinFilters = map[string]string{"eda.builtin.json_filter": "ansible.eda.json_filter", "eda.builtin.normalize_keys": "ansible.eda.normalize_keys",
		"eda.builtin.insert_hosts_to_meta": "ansible.eda.insert_hosts_to_meta", "eda.builtin.noop": "ansible.eda.noop",
		"eda.builtin.dashes_to_underscores": "ansible.eda.dashes_to_underscores", "ansible.builtin.dashes_to_underscores": "ansible.eda.dashes_to_underscores"}
	pluginName = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
	varName    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	durationRe = regexp.MustCompile(`^(\d+)\s+(millisecond|second|minute|hour|day)s?$`)
)

// SourceInfo is a rulebook's source as EDA lists it.
type SourceInfo struct {
	Name, Plugin string
	Args         map[string]any
}

// Sources are a rulebook's sources, in order, by their SourceName.
func Sources(text []byte) ([]SourceInfo, error) {
	sets, err := parse(text)
	if err != nil {
		return nil, err
	}
	var out []SourceInfo
	for _, rs := range sets {
		for _, s := range rs.Sources {
			out = append(out, SourceInfo{Name: sourceName(s, len(out)+1), Plugin: s.Plugin, Args: s.Args})
		}
	}
	return out, nil
}

// sourceName is a source's name as EDA gives it: its own, or __SOURCE_n
// for the rulebook's nth source.
func sourceName(s source, n int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("__SOURCE_%d", n)
}

// Translate translates a rulebook. An error is a rulebook it can't read
// at all; a piece it can't translate is in the report, with the reason.
func Translate(text []byte, o Options) (*Plan, error) {
	sets, err := parse(text)
	if err != nil {
		return nil, err
	}
	if o.Variables == nil {
		o.Variables = map[string]any{}
	}
	t := &translation{o: o, plan: &Plan{Report: &Report{}}, sets: map[string]*rsCtx{}}
	var ctxs []*rsCtx
	for i := range sets {
		rc := t.sources(&sets[i])
		t.sets[sets[i].Name] = rc
		ctxs = append(ctxs, rc)
	}
	for _, rc := range ctxs {
		for _, r := range rc.rs.Rules {
			for _, a := range r.Actions {
				posts := a.Kind == "post_event" || a.Kind == "set_fact"
				if a.Kind == "run_job_template" || a.Kind == "run_workflow_template" {
					p, _ := a.Args["post_events"].(bool)
					f, _ := a.Args["set_facts"].(bool)
					posts = p || f
				}
				if posts {
					target := rc
					if name := str(a.Args["ruleset"]); name != "" {
						target = t.sets[name]
					}
					if target != nil {
						target.posted = true
					}
				}
			}
		}
	}
	for _, rc := range ctxs {
		t.rules(rc)
	}
	t.plan.Report.Finish()
	return t.plan, nil
}

type translation struct {
	o    Options
	plan *Plan
	sets map[string]*rsCtx
	// sourceN counts the rulebook's sources, for their SourceName.
	sourceN int
}

// rsCtx is a ruleset being translated.
type rsCtx struct {
	rs     *ruleset
	report *RulesetReport
	kind   string
	// mixed is why the ruleset's rules can't be translated: sources of
	// different kinds.
	mixed      string
	hostPath   []string
	streams    []string
	main       string
	posted     bool
	hosts      string
	ttl        int64
	exclusions []string
	notes      []string
	// live are the branches the rule's condition can match: the others'
	// events never reach its actions. nil: all of them.
	live, matched []bool
}

func (t *translation) name(parts ...string) string {
	n := t.o.Name + ": " + strings.Join(parts, " / ")
	if len(n) > 512 {
		n = n[:509] + "..."
	}
	return n
}

func (t *translation) sources(rs *ruleset) *rsCtx {
	rep := &RulesetReport{Name: rs.Name, Sources: []*SourceReport{}, Rules: []*RuleReport{}}
	t.plan.Report.Rulesets = append(t.plan.Report.Rulesets, rep)
	rc := &rsCtx{rs: rs, report: rep, ttl: DefaultEventsTTL}
	kinds := map[string]bool{}
	for i, s := range rs.Sources {
		t.sourceN++
		sr := &SourceReport{Name: s.Name, Plugin: s.Plugin, Status: Translated}
		if sr.Name == "" {
			sr.Name = s.Plugin
		}
		rep.Sources = append(rep.Sources, sr)
		if id, ok := t.o.SourceStreams[sourceName(s, t.sourceN)]; ok {
			kinds[KindWebhook] = true
			key := fmt.Sprintf("stream:%s/%d", rs.Name, i)
			t.plan.Streams = append(t.plan.Streams, Stream{Key: key, Name: t.name(rs.Name, sr.Name), Type: StreamGeneric, Ruleset: rs.Name, Existing: id})
			rc.streams = append(rc.streams, key)
			sr.Key, sr.As = key, "event_stream:existing"
			sr.Notes = append(sr.Notes, fmt.Sprintf("Mapped to event stream %d: the stream's events reach the ruleset's rules instead of the source's, as a webhook's do.", id))
			continue
		}
		if s.err != nil {
			sr.Status, sr.Reasons = NotTranslated, []string{"The source can't be read: " + s.err.Error() + "."}
			continue
		}
		used := s.Args
		if webhookPlugins[s.Plugin] || alertmanagerPlugins[s.Plugin] {
			// The stream's receiver replaces the plugin's server.
			used = map[string]any{}
			for k, v := range s.Args {
				if k != "host" && k != "port" {
					used[k] = v
				}
			}
		}
		args, err := renderArgs(used, t.o.Variables)
		if err != nil {
			sr.Status, sr.Reasons = NotTranslated, []string{"Its arguments: " + clause(err)}
			continue
		}
		label := sr.Name
		switch {
		case webhookPlugins[s.Plugin]:
			kinds[KindWebhook] = true
			key := fmt.Sprintf("stream:%s/%d", rs.Name, i)
			t.plan.Streams = append(t.plan.Streams, Stream{Key: key, Name: t.name(rs.Name, label), Type: StreamGeneric, Ruleset: rs.Name})
			rc.streams = append(rc.streams, key)
			sr.Key = key
			sr.As = "event_stream:" + StreamGeneric
			sr.Notes = append(sr.Notes, "Senders post to the stream's receiver URL rather than the plugin's host and port, and authenticate with the stream's credential: set one before the stream takes events.")
			if args["token"] != nil || args["hmac_secret"] != nil {
				sr.Notes = append(sr.Notes, "The plugin's token and HMAC secret aren't imported: they are the stream credential's.")
			}
			streamFilters(s, sr)
		case alertmanagerPlugins[s.Plugin]:
			kinds[KindAlertmanager] = true
			if p := str(args["data_alerts_path"]); args["data_alerts_path"] != nil && p != "alerts" {
				sr.Status, sr.Reasons = NotTranslated, []string{fmt.Sprintf("data_alerts_path %q: an Alertmanager stream reads the alerts of Alertmanager's notifications, under alerts.", p)}
				continue
			}
			sep := "."
			if args["data_path_separator"] != nil {
				sep = str(args["data_path_separator"])
			}
			hp := "labels.instance"
			if args["data_host_path"] != nil {
				hp = str(args["data_host_path"])
			}
			if hp != "" && sep != "" {
				rc.hostPath = strings.Split(hp, sep)
			}
			key := fmt.Sprintf("stream:%s/%d", rs.Name, i)
			t.plan.Streams = append(t.plan.Streams, Stream{Key: key, Name: t.name(rs.Name, label), Type: StreamAlertmanager, Ruleset: rs.Name})
			rc.streams = append(rc.streams, key)
			sr.Key = key
			sr.As = "event_stream:" + StreamAlertmanager
			sr.Notes = append(sr.Notes, "Alertmanager posts to the stream's receiver URL rather than the plugin's host and port, and authenticates with the stream's credential: set one before the stream takes alerts.",
				"The stream gives rules each alert, not the notification that carries them (skip_original_data: true), and sends an alert again only when its status or start changes.")
			streamFilters(s, sr)
		default:
			kinds[KindConnector] = true
			plugin := s.Plugin
			if p, ok := builtinSources[plugin]; ok {
				sr.Notes = append(sr.Notes, fmt.Sprintf("%s is part of ansible-rulebook: the connector runs %s, its twin in the ansible.eda collection.", plugin, p))
				plugin = p
			}
			if !pluginName.MatchString(plugin) {
				sr.Status, sr.Reasons = NotTranslated, []string{fmt.Sprintf("%s is not a collection's plugin (namespace.collection.name): a connector runs source plugins from collections, not from a directory of sources (ansible-rulebook -S).", plugin)}
				continue
			}
			var filters []any
			for _, f := range s.Filters {
				name, fargs, err := filterOf(f)
				if err == nil {
					if p, ok := builtinFilters[name]; ok {
						name = p
					}
					if !pluginName.MatchString(name) {
						err = refuse("the filter %s is not a collection's plugin", name)
					}
				}
				if err != nil {
					sr.Status = Partial
					sr.Reasons = append(sr.Reasons, "A filter isn't translated: "+clause(err))
					continue
				}
				filters = append(filters, map[string]any{name: fargs})
			}
			key := fmt.Sprintf("connector:%s/%d", rs.Name, i)
			t.plan.Connectors = append(t.plan.Connectors, Connector{Key: key, Name: t.name(rs.Name, label), Stream: "stream:" + rs.Name,
				Source: plugin, Args: args, Filters: filters})
			sr.Key = key
			sr.As = "event_connector:" + plugin
			sr.Notes = append(sr.Notes, "The connector starts disabled: give it an execution environment with the plugin's collection, and its credentials, then enable it.")
		}
	}
	if kinds[KindConnector] || len(rc.streams) == 0 {
		key := "stream:" + rs.Name
		t.plan.Streams = append(t.plan.Streams, Stream{Key: key, Name: t.name(rs.Name), Type: StreamCloudEvents, Ruleset: rs.Name})
		rc.streams = append([]string{key}, rc.streams...)
	}
	rc.main = rc.streams[0]
	if len(rs.Sources) > 0 && !slices.ContainsFunc(rep.Sources, func(s *SourceReport) bool { return s.Status != NotTranslated }) {
		rep.Notes = append(rep.Notes, "None of the ruleset's sources is translated: its rules take the events posted to its stream only.")
	}
	var ks []string
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	switch len(ks) {
	case 0:
		rc.kind = KindConnector
	case 1:
		rc.kind = ks[0]
	default:
		rc.kind = ks[0]
		rc.mixed = fmt.Sprintf("The ruleset's sources are of different kinds (%s): their events have different shapes, and awxnext translates a ruleset's conditions for one.", strings.Join(ks, ", "))
	}
	if len(ks) > 1 && kinds[KindWebhook] && kinds[KindAlertmanager] && len(ks) == 2 {
		rc.mixed = "The ruleset reads a webhook and Alertmanager: their events have different shapes, and awxnext translates a ruleset's conditions for one."
	}
	rc.hosts = hostsText(rs.Hosts)
	t.options(rc)
	return rc
}

func streamFilters(s source, sr *SourceReport) {
	for _, f := range s.Filters {
		name, _, err := filterOf(f)
		if err != nil {
			name = "a filter"
		}
		sr.Status = Partial
		sr.Reasons = append(sr.Reasons, fmt.Sprintf("The filter %s isn't applied: a stream keeps the events as they come, and runs no event filter.", name))
	}
}

func filterOf(f any) (string, any, error) {
	m, ok := f.(map[string]any)
	if !ok || len(m) != 1 {
		return "", nil, refuse("a filter is a mapping of its name to its arguments")
	}
	for k, v := range m {
		if v == nil {
			v = map[string]any{}
		}
		if _, ok := v.(map[string]any); !ok {
			return k, nil, refuse("the arguments of %s are not a mapping", k)
		}
		return k, v, nil
	}
	return "", nil, nil
}

func hostsText(h any) string {
	switch x := h.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = str(e)
		}
		return strings.Join(parts, ",")
	case nil:
		return "all"
	}
	return str(h)
}

func (t *translation) options(rc *rsCtx) {
	opts := rc.rs.Options
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	note := func(s string) { rc.report.Notes = append(rc.report.Notes, s) }
	strategy := "sequential"
	for _, k := range keys {
		v := opts[k]
		switch k {
		case "match_multiple_rules":
		case "execution_strategy":
			strategy = str(v)
		case "default_events_ttl":
			secs, n, err := duration(v)
			if err != nil {
				note("default_events_ttl: " + clause(err) + " The patterns' windows are 2 hours.")
				continue
			}
			if n != "" {
				note("default_events_ttl: " + n)
			}
			rc.ttl = secs
		case "gather_facts":
			if b, _ := v.(bool); b {
				note("gather_facts isn't translated: awxnext's rules don't see the hosts' facts.")
			}
		default:
			note(fmt.Sprintf("The option %s isn't translated: ansible-rulebook 1.3 doesn't know it.", k))
		}
	}
	if len(rc.rs.Rules) > 1 {
		if b, _ := opts["match_multiple_rules"].(bool); b {
			note("match_multiple_rules: each rule acts on every event it matches, as each awxnext rule does.")
		} else {
			note("match_multiple_rules is false: an event goes to the first rule it matches. Each rule's filter leaves out the events of the rules before it.")
		}
	}
	launches := slices.ContainsFunc(rc.rs.Rules, func(r rule) bool {
		return slices.ContainsFunc(r.Actions, func(a action) bool { return a.Kind == "run_job_template" || a.Kind == "run_workflow_template" })
	})
	switch {
	case !launches:
	case strategy == "parallel":
		note("execution_strategy parallel: awxnext's rules act as their events come, without waiting for each other's jobs.")
	default:
		note("execution_strategy sequential: ansible-rulebook runs a ruleset's actions one at a time, waiting for each job. awxnext's rules act as their events come, without waiting for the jobs before: bound them with max_concurrent_runs or host_lock if they must not overlap.")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderArgs renders a source's arguments with the rulebook's variables,
// as ansible-rulebook does when it starts.
func renderArgs(args map[string]any, vars map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range sortedKeys(args) {
		r, err := renderValue(args[k], vars)
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

var jinjaVar = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*(?:\|\s*default\(([^()]*)\)\s*)?\}\}`)

// renderVar is the value of a {{ variable }}, or {{ variable | default(v) }}.
func renderVar(m []string, vars map[string]any) (any, error) {
	val, ok := lookupVar(vars, toAny(strings.Split(m[1], ".")))
	if ok {
		return val, nil
	}
	if m[2] != "" {
		if n, err := parseCondition(strings.TrimSpace(m[2])); err == nil {
			if l, isLit := n.(*literal); isLit {
				return l.v, nil
			}
		}
		return nil, refuse("the default of %s is not a value", m[1])
	}
	return nil, refuse("the variable %s: the rulebook's variables don't set it", m[1])
}

func renderValue(v any, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if !strings.Contains(x, "{{") && !strings.Contains(x, "{%") {
			return x, nil
		}
		if whole := jinjaVar.FindStringSubmatch(strings.TrimSpace(x)); whole != nil && whole[0] == strings.TrimSpace(x) {
			return renderVar(whole, vars)
		}
		var err error
		out := jinjaVar.ReplaceAllStringFunc(x, func(m string) string {
			val, e := renderVar(jinjaVar.FindStringSubmatch(m), vars)
			if e != nil {
				err = e
			}
			return str(val)
		})
		if err != nil {
			return nil, err
		}
		if strings.Contains(out, "{{") || strings.Contains(out, "{%") {
			return nil, refuse("the Jinja in %q: awxnext renders {{ variable }} and default(value) only", x)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := renderValue(e, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		return renderArgs(x, vars)
	}
	return v, nil
}

// reason is an error as a sentence of the report.
func reason(err error) string { return sentence(clause(err)) }

// clause is an error as the end of a sentence.
func clause(err error) string {
	var u *untranslatable
	s := err.Error()
	if errors.As(err, &u) {
		s = u.reason
	}
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

var plainWord = regexp.MustCompile(`^[a-z]+\b`)

// sentence capitalizes s, unless it starts with a name: awxnext, or an
// action, option or plugin.
func sentence(s string) string {
	w := plainWord.FindString(s)
	if w == "" || w == "awxnext" || len(s) > len(w) && strings.ContainsAny(s[len(w):len(w)+1], "_.") {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// duration reads a time like 5 minutes, in seconds; a note when it rounds.
func duration(v any) (int64, string, error) {
	s := strings.TrimSpace(str(v))
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, "", refuse("%q is not a duration such as 5 minutes", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > 1<<40 {
		return 0, "", refuse("%q is too long", s)
	}
	var secs int64
	note := ""
	switch m[2] {
	case "millisecond":
		secs = (n + 999) / 1000
		if n%1000 != 0 {
			note = fmt.Sprintf("%s is rounded up to %d seconds: awxnext's windows count whole seconds.", s, max(secs, 1))
		}
		secs = max(secs, 1)
	case "second":
		secs = n
	case "minute":
		secs = n * 60
	case "hour":
		secs = n * 3600
	case "day":
		secs = n * 86400
	}
	if secs > maxWindow {
		return 0, "", refuse("%s is longer than awxnext's longest window, 7 days", s)
	}
	if secs < 1 {
		return 0, "", refuse("%s is shorter than a second", s)
	}
	return secs, note, nil
}

// branch is one shape of event a ruleset's rules read, and the condition
// that tells it.
type branch struct {
	v     view
	guard func(base string) string
}

// branches are the shapes of event the rule's actions get.
func (rc *rsCtx) branches() []branch {
	all := rc.allBranches()
	if len(rc.live) != len(all) {
		return all
	}
	var out []branch
	for i, b := range all {
		if rc.live[i] {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return all
	}
	if len(out) == 1 {
		out[0].guard = func(string) string { return "true" }
	}
	return out
}

func (rc *rsCtx) allBranches() []branch {
	v := view{kind: rc.kind, hostPath: rc.hostPath, base: "event"}
	if !rc.posted || rc.kind == KindConnector {
		return []branch{{v: v, guard: func(string) string { return "true" }}}
	}
	posted := func(base string) string {
		return fmt.Sprintf("%s.source.startsWith(%s)", base, celString(postedPrefix))
	}
	return []branch{
		{v: view{kind: KindConnector, base: "event"}, guard: posted},
		{v: v, guard: func(base string) string { return not(posted(base)) }},
	}
}

func (rc *rsCtx) condition(n node, vars map[string]any) (string, error) {
	out := "false"
	all := rc.allBranches()
	if len(rc.matched) != len(all) {
		rc.matched = make([]bool, len(all))
	}
	for i, b := range all {
		c := &condCtx{v: b.v, vars: vars}
		s, err := c.boolean(n)
		if err != nil {
			return "", err
		}
		if s != "false" {
			rc.matched[i] = true
		}
		out = or(out, and(b.guard("event"), s))
	}
	return out, nil
}

// actValue translates an action's argument for each shape of event.
func (rc *rsCtx) actValue(v any, vars map[string]any, conds int, names map[string]int) (string, error) {
	bs := rc.branches()
	var outs []string
	for _, b := range bs {
		a := &actCtx{v: b.v, vars: vars, conds: conds, names: names}
		s, err := a.value(v)
		if err != nil {
			return "", err
		}
		outs = append(outs, s)
	}
	if len(outs) == 1 || outs[0] == outs[1] {
		return outs[0], nil
	}
	base := "event"
	if conds > 0 {
		base = "events[0]"
	}
	return fmt.Sprintf("(%s ? %s : %s)", bs[0].guard(base), outs[0], outs[1]), nil
}

// shape is what a rule's condition became.
type shape struct {
	filter     string
	pattern    string
	conditions []string
	window     int64
	// conds is how many events the rule fires with: 0 for one.
	conds int
	names map[string]int
	// single: the rule takes each event it matches on its own, and leaves
	// it to no later rule.
	single bool
	notes  []string

	throttle     *throttled
	patternCount int64
	key          string
}

func (t *translation) shape(rc *rsCtx, r rule) (*shape, error) {
	vars := t.o.Variables
	parseAll := func(list []any) ([]node, error) {
		var out []node
		for _, item := range list {
			text := str(item)
			if b, ok := item.(bool); ok {
				text = strconv.FormatBool(b)
			}
			n, err := parseCondition(text)
			if err != nil {
				return nil, refuse("the condition %q can't be read: %v", text, err)
			}
			out = append(out, n)
		}
		return out, nil
	}
	sh := &shape{}
	switch c := r.Condition.(type) {
	case string, bool:
		nodes, err := parseAll([]any{c})
		if err != nil {
			return nil, err
		}
		if sh.filter, err = rc.condition(nodes[0], vars); err != nil {
			return nil, err
		}
		sh.single = true
		return sh, nil
	case map[string]any:
		var when string
		var list []any
		for k, v := range c {
			if k == "timeout" {
				continue
			}
			if when != "" {
				return nil, refuse("a condition has one of all, any and not_all")
			}
			when = k
			list, _ = v.([]any)
		}
		if when != "all" && when != "any" && when != "not_all" {
			return nil, refuse("a condition has one of all, any and not_all")
		}
		if len(list) == 0 {
			return nil, refuse("%s has no condition", when)
		}
		nodes, err := parseAll(list)
		if err != nil {
			return nil, err
		}
		var conds []string
		names := map[string]int{}
		for i, n := range nodes {
			if a, ok := n.(*assignNode); ok {
				names[a.name] = i
			}
			s, err := rc.condition(n, vars)
			if err != nil {
				return nil, err
			}
			conds = append(conds, s)
		}
		if when == "any" || len(nodes) == 1 {
			sh.filter = "false"
			for _, s := range conds {
				sh.filter = or(sh.filter, s)
			}
			sh.single = true
			if c["timeout"] != nil {
				sh.notes = append(sh.notes, "The timeout of a rule that fires with one event has no effect.")
			}
			return sh, nil
		}
		if len(nodes) > rules.MaxConditions {
			return nil, refuse("%d conditions: an awxnext pattern has %d at most", len(nodes), rules.MaxConditions)
		}
		window := rc.ttl
		if c["timeout"] != nil {
			secs, n, err := duration(c["timeout"])
			if err != nil {
				return nil, err
			}
			if n != "" {
				sh.notes = append(sh.notes, "timeout: "+n)
			}
			window = secs
		}
		sh.filter = "false"
		for _, s := range conds {
			sh.filter = or(sh.filter, s)
		}
		sh.conditions, sh.window, sh.conds, sh.names = conds, window, len(conds), names
		if when == "not_all" {
			if c["timeout"] == nil {
				return nil, refuse("not_all needs a timeout")
			}
			if len(nodes) != 2 {
				return nil, refuse("not_all with %d conditions: awxnext's absence pattern waits for one event after another, so it translates not_all with 2", len(nodes))
			}
			sh.pattern = rules.PatternAbsence
			sh.notes = append(sh.notes, "not_all is an absence pattern: its window opens on an event of the first condition only, where ansible-rulebook opens it on either.")
			return sh, nil
		}
		sh.pattern = rules.PatternAll
		if c["timeout"] == nil {
			sh.notes = append(sh.notes, fmt.Sprintf("The pattern's window is the ruleset's default_events_ttl, %d seconds: how long ansible-rulebook keeps a partial match.", window))
		}
		sh.notes = append(sh.notes, "ansible-rulebook matches each event with one condition; an awxnext pattern can count an event for several.")
		return sh, nil
	case nil:
		return nil, refuse("the rule has no condition")
	}
	return nil, refuse("the condition is neither a string, a boolean, nor all, any or not_all")
}

func (t *translation) rules(rc *rsCtx) {
	seen := map[string]bool{}
	for _, r := range rc.rs.Rules {
		rr := &RuleReport{Name: r.Name, Actions: []*ActionReport{}}
		rc.report.Rules = append(rc.report.Rules, rr)
		for _, a := range r.Actions {
			rr.Actions = append(rr.Actions, &ActionReport{Action: a.Kind, Status: NotTranslated})
		}
		fail := func(why string) {
			rr.Reasons = append(rr.Reasons, why)
			for _, a := range rr.Actions {
				if a.Reason == "" {
					a.Reason = "The rule isn't translated."
				}
				a.Status = NotTranslated
			}
		}
		switch {
		case r.err != nil:
			fail("The rule can't be read: " + r.err.Error() + ".")
			continue
		case seen[r.Name]:
			fail("A rule before it has the same name: ansible-rulebook refuses the rulebook.")
			continue
		}
		seen[r.Name] = true
		if !r.Enabled {
			fail("The rule is disabled in the rulebook (enabled: false): ansible-rulebook doesn't load it.")
			continue
		}
		if rc.mixed != "" {
			fail(rc.mixed)
			continue
		}
		rc.live, rc.matched = nil, nil
		sh, err := t.shape(rc, r)
		rc.live = rc.matched
		if err != nil {
			fail("Its condition: " + clause(err))
			for _, n := range rc.notes {
				rr.Notes = append(rr.Notes, n)
			}
			if b, _ := rc.rs.Options["match_multiple_rules"].(bool); !b {
				rc.notes = append(rc.notes, fmt.Sprintf("Rule %q comes before this one and isn't translated: the events it would take reach this rule.", r.Name))
			}
			continue
		}
		rr.Notes = append(rr.Notes, sh.notes...)
		own := sh.filter
		for _, e := range rc.exclusions {
			sh.filter = and(sh.filter, not(paren(e)))
		}
		rr.Notes = append(rr.Notes, rc.notes...)
		if b, _ := rc.rs.Options["match_multiple_rules"].(bool); !b {
			if sh.single {
				rc.exclusions = append(rc.exclusions, own)
			} else {
				rc.notes = append(rc.notes, fmt.Sprintf("Rule %q comes before this one and waits for several events: ansible-rulebook gives it the events it matches first, awxnext gives them to this rule too.", r.Name))
			}
		}
		if err := t.throttle(rc, r, sh); err != nil {
			fail("Its throttle: " + clause(err))
			continue
		}
		t.actions(rc, r, rr, sh)
	}
}

// throttled is a rule's throttle and key.
type throttled struct {
	kind   string
	window int64
	key    string
}

func (t *translation) throttle(rc *rsCtx, r rule, sh *shape) error {
	th := r.Throttle
	if th == nil {
		return nil
	}
	group, _ := th["group_by_attributes"].([]any)
	if len(group) == 0 {
		return refuse("group_by_attributes is required")
	}
	var parts []string
	for _, g := range group {
		n, err := parseCondition(str(g))
		if err != nil {
			return refuse("group_by_attributes %q: %v", str(g), err)
		}
		p, ok := n.(*pathNode)
		if !ok || p.root != "event" && p.root != "fact" {
			return refuse("group_by_attributes %q is not an attribute of the event", str(g))
		}
		var part string
		for _, b := range rc.branches() {
			r, err := b.v.resolve(p.parts)
			if err != nil {
				return err
			}
			s := `""`
			if !r.absent {
				v := r.value()
				s = fmt.Sprintf(`(%s ? (type(%s) == list ? %s.map(x, string(x)).join(",") : string(%s)) : "")`, r.guardExpr(), v, v, v)
			}
			if part == "" {
				part = s
			} else if part != s {
				part = fmt.Sprintf("(%s ? %s : %s)", rc.branches()[0].guard("event"), part, s)
			}
		}
		parts = append(parts, part)
	}
	key := strings.Join(parts, ` + "|" + `)
	var kinds []string
	for _, k := range []string{"once_within", "once_after", "accumulate_within"} {
		if th[k] != nil {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) != 1 {
		return refuse("a throttle has one of once_within, once_after and accumulate_within")
	}
	if sh.pattern != "" {
		return refuse("a rule with several conditions acts once per window in awxnext: it takes no throttle")
	}
	secs, note, err := duration(th[kinds[0]])
	if err != nil {
		return err
	}
	if note != "" {
		sh.notes = append(sh.notes, kinds[0]+": "+note)
	}
	sh.throttle = &throttled{window: secs, key: key}
	switch kinds[0] {
	case "once_within":
		sh.throttle.kind = rules.OnceWithin
	case "once_after":
		sh.throttle.kind = rules.OnceAfter
	case "accumulate_within":
		n, ok := th["threshold"].(int64)
		if !ok || n < 1 {
			return refuse("accumulate_within needs a threshold of 1 or more")
		}
		sh.throttle = nil
		if n == 1 {
			sh.notes = append(sh.notes, "accumulate_within with a threshold of 1 acts on each event.")
			return nil
		}
		sh.pattern, sh.patternCount, sh.window = rules.PatternCount, n, secs
		sh.key = key
		sh.notes = append(sh.notes, fmt.Sprintf("accumulate_within is a count pattern: the rule acts once %d events of a key came within the window.", n))
	}
	return nil
}

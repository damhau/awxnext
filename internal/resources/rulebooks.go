package resources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/rulebooks"
)

// Rulebooks (roadmap M14-09): ansible-rulebook's rulebooks, translated by
// internal/rulebooks into event streams, connectors and rules. A rulebook
// belongs to a project, as in EDA: those of its files, found again at each
// sync, and those uploaded to it. Each import makes or updates the objects
// of the plan, tied to the rulebook by their rulebook and rulebook_key,
// and deletes those whose piece is gone. What an import translated is the
// rulebook's: it changes with the rulebook only. What an operator decides
// (enabling, the mode, guardrails, credentials) is theirs, and an import
// keeps it.

// RulebookDirs are where a project's rulebooks are, as EDA's controller
// looks for them.
var RulebookDirs = []string{"extensions/eda/rulebooks", "rulebooks"}

// maxProjectRulebooks bounds the rulebooks a sync imports from a project.
const maxProjectRulebooks = 200

// The fields of imported objects that operators change: the others come
// from the rulebook.
var (
	ruleOperatorFields = []string{"description", "enabled", "mode", "max_concurrent_runs", "host_lock", "max_group_share",
		"breaker_firings", "breaker_window", "breaker_notification_template", "window_action", "approval_timeout",
		"check", "check_condition", "check_window", "check_template", "check_notification_template", "check_approval"}
	streamOperatorFields    = []string{"description", "credential", "test_mode", "retention_days", "max_payload_bytes", "rate_limit", "mapping"}
	connectorOperatorFields = []string{"description", "enabled", "credentials", "execution_environment", "instance_group", "python",
		"restart_policy", "restart_delay", "max_restarts", "buffer_size"}
)

func rulebookField() *Field {
	return &Field{Name: "rulebook", Type: api.TFK, Ref: "rulebooks", ReadOnly: true, Nullable: true,
		Help: "The rulebook the object was imported from: what the rulebook says changes with it only."}
}

func rulebookKeyField() *Field {
	return &Field{Name: "rulebook_key", Type: api.TString, ReadOnly: true, Hidden: true, NoFilter: true}
}

// importing says whether c is an import's.
func importing(c *Ctx) bool { return c.Extra != nil && c.Extra["rulebook"] != nil }

// lockImported refuses a change by hand to what an imported object took
// from its rulebook.
func lockImported(c *Ctx, res *Resource, old, data Obj, free []string) error {
	if old == nil || importing(c) || old["rulebook"] == nil {
		return nil
	}
	var locked []string
	for k, v := range data {
		if slices.Contains(free, k) {
			continue
		}
		if f := fieldOf(res, k); f == nil || f.ReadOnly {
			continue
		}
		if !sameValue(old[k], v) {
			locked = append(locked, k)
		}
	}
	if len(locked) == 0 {
		return nil
	}
	sort.Strings(locked)
	return api.Detail(409, fmt.Sprintf("This %s comes from rulebook %v: change %s in the rulebook, and import it again.",
		strings.ToLower(res.Verbose), old["rulebook"], strings.Join(locked, ", ")))
}

func fieldOf(res *Resource, name string) *Field {
	for _, f := range res.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// refuseImportedDelete refuses to delete by hand an object an import made:
// the next import would make it again.
func refuseImportedDelete(c *Ctx, what string, obj Obj) error {
	if obj["rulebook"] == nil || importing(c) {
		return nil
	}
	return api.Detail(409, fmt.Sprintf("This %s comes from rulebook %v: remove it from the rulebook, or delete the rulebook.", what, obj["rulebook"]))
}

// saveImportedRule checks a rule an import saves. A new rule starts
// disabled, without an owner; an enabled one keeps its owner, whose rights
// the new translation must still have, or it stops.
func saveImportedRule(c *Ctx, old, data Obj, r *eventRule) error {
	enabled := boolv(data["enabled"])
	if _, ok := data["enabled"]; !ok && old != nil {
		enabled = boolv(old["enabled"])
	}
	if !enabled {
		return checkRuleTargets(c, r)
	}
	stop := func(reason string) error {
		data["enabled"] = false
		data["stopped_reason"] = "The rulebook's import stopped the rule: " + reason
		return checkRuleTargets(c, r)
	}
	if r.Owner == nil {
		return stop("it has no owner.")
	}
	p, err := auth.LoadPrincipal(c, c.App, *r.Owner)
	if err != nil {
		return err
	}
	if p == nil {
		return stop(fmt.Sprintf("its owner (user %d) is no longer active.", *r.Owner))
	}
	oc := *c
	oc.User = p
	if err := checkRuleTargets(&oc, r); err != nil {
		return err
	}
	if err := checkRuleGuardrails(&oc, r); err != nil {
		return err
	}
	reason, err := ruleRefusal(&oc, r)
	if err != nil {
		return err
	}
	if reason != "" {
		return stop(reason)
	}
	return nil
}

func registerRulebooks() {
	proj := api.Lookup("projects")
	projectPerm := func(c *Ctx, project any, perm string) error {
		id, ok := api.ID(project)
		if !ok {
			return api.FieldError("project", "This field is required.")
		}
		return api.RequirePermID(c, "project", "projects", id, perm)
	}
	res := api.Register(&Resource{
		Name:     "rulebooks",
		Type:     "rulebook",
		Table:    "rulebooks",
		Verbose:  "Rulebook",
		Ordering: "name",
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Nullable: true, Help: "The project's organization."},
			{Name: "project", Type: api.TFK, Ref: "projects", Required: true, CreateOnly: true,
				Help: "The project the rulebook belongs to: its files, or uploaded to it."},
			{Name: "path", Type: api.TString, ReadOnly: true,
				Help: "The rulebook's file in the project, which each sync imports again. Empty: an uploaded rulebook."},
			{Name: "rulesets", Type: api.TText, MaxLength: rulebooks.MaxSize, Help: "The rulebook's YAML: its rulesets."},
			{Name: "variables", Type: api.TJSON, Default: Obj{}, Validate: validateRulebookVars,
				Help: "The rulebook's variables, as ansible-rulebook's --vars: vars.name in conditions, {{ name }} in the arguments of sources and actions."},
			{Name: "revision", Type: api.TString, ReadOnly: true, Help: "The project's revision the file was imported from."},
			{Name: "imported", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When the rulebook was last imported."},
			{Name: "import_error", Type: api.TText, ReadOnly: true,
				Help: "Why the last import could not read the rulebook: the objects of the import before stay as they were."},
			{Name: "report", Type: api.TJSON, ReadOnly: true,
				Help: "What became of each source, rule and action of the rulebook: translated or not, with the reasons, and the objects made."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.project_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type = 'project' AND (" + viewOf(c, proj, arg) + "))"
		},
		CanAdd:    func(c *Ctx, data Obj) error { return projectPerm(c, data["project"], "change") },
		CanChange: func(c *Ctx, old, data Obj) error { return projectPerm(c, old["project"], "change") },
		CanDelete: func(c *Ctx, obj Obj) error {
			if err := projectPerm(c, obj["project"], "change"); err != nil {
				return err
			}
			if str(obj["path"]) != "" && !importing(c) {
				return api.Detail(409, "This rulebook is a file of its project: remove the file, and sync the project.")
			}
			return nil
		},
		// A rulebook is seen by whoever sees its project, and changed by
		// whoever may change it.
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			id, _ := api.ID(obj["project"])
			if perm == "view" {
				return api.CanView(c, proj, id)
			}
			ok, err := api.HasPermID(c, "projects", id, "change")
			if errors.Is(err, api.ErrNotFound) {
				return false, nil
			}
			return ok, err
		},
		BeforeSave:   saveRulebook,
		AfterCreate:  func(c *Ctx, obj Obj) error { return importAfterSave(c, nil, obj) },
		AfterUpdate:  func(c *Ctx, old, obj Obj) error { return importAfterSave(c, old, obj) },
		BeforeDelete: deleteRulebookObjects,
		Actions: []*api.Action{{Name: "import", Methods: []string{"POST"}, Perm: "change", Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			err := c.WithTx(func(c *Ctx) error { return ImportRulebook(c, id, c.User) })
			if err != nil {
				api.WriteError(w, err)
				return
			}
			writeRulebook(c, w, id)
		}}},
	})
	res.SubLists = []*SubList{
		readOnlyChildSub("event_streams", "event_streams", "rulebook_id"),
		readOnlyChildSub("event_connectors", "event_connectors", "rulebook_id"),
		readOnlyChildSub("event_rules", "event_rules", "rulebook_id"),
	}
	proj.SubLists = append(proj.SubLists, childSub("rulebooks", "rulebooks", "project_id", "project"))
	proj.Actions = append(proj.Actions, &api.Action{Name: "import_rulebooks", Methods: []string{"POST"}, Perm: "change",
		Handler: importProjectRulebooksView})
}

func writeRulebook(c *Ctx, w http.ResponseWriter, id int64) {
	res := api.Lookup("rulebooks")
	obj, err := api.Get(c, res, id)
	if err == nil {
		obj, err = api.SerializeOne(c, res, obj)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, obj)
}

func validateRulebookVars(c *Ctx, v any, data Obj) error {
	if _, ok := v.(map[string]any); !ok && v != nil {
		return errors.New("Expected an object: the rulebook's variables by name.")
	}
	return nil
}

func saveRulebook(c *Ctx, old, data Obj) error {
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	if old != nil && str(old["path"]) != "" && !importing(c) {
		for _, f := range []string{"name", "rulesets"} {
			if v, ok := data[f]; ok && !sameValue(old[f], v) {
				return api.Detail(409, fmt.Sprintf("This rulebook is %s of its project: change %s in the file, and sync the project.", str(old["path"]), f))
			}
		}
	}
	pid, _ := api.ID(merged["project"])
	var org *int64
	if err := c.DB().QueryRow(c, "SELECT organization_id FROM unified_job_templates WHERE id = $1 AND type = 'project'", pid).Scan(&org); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return api.FieldError("project", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", pid))
		}
		return err
	}
	if org == nil {
		return api.FieldError("project", "The project has no organization: a rulebook's streams and rules need one.")
	}
	data["organization"] = *org
	if str(merged["path"]) != "" {
		return nil
	}
	if strings.TrimSpace(str(merged["rulesets"])) == "" {
		return api.FieldError("rulesets", "An uploaded rulebook needs its YAML.")
	}
	if _, ok := data["rulesets"]; ok || old == nil {
		vars, _ := merged["variables"].(map[string]any)
		if _, err := rulebooks.Translate([]byte(str(merged["rulesets"])), rulebooks.Options{Name: str(merged["name"]), Variables: vars}); err != nil {
			return api.FieldError("rulesets", "The rulebook can't be read: "+err.Error()+".")
		}
	}
	return nil
}

// importAfterSave imports a rulebook whose YAML or variables a person
// changed.
func importAfterSave(c *Ctx, old, obj Obj) error {
	if importing(c) {
		return nil
	}
	if old != nil && sameValue(old["rulesets"], obj["rulesets"]) && sameValue(old["variables"], obj["variables"]) {
		return nil
	}
	id, _ := api.ID(obj["id"])
	if err := ImportRulebook(c, id, c.User); err != nil {
		return err
	}
	// The response shows the import's report.
	fresh, err := api.Get(c, api.Lookup("rulebooks"), id)
	if err != nil {
		return err
	}
	for k, v := range fresh {
		obj[k] = v
	}
	return nil
}

// deleteRulebookObjects deletes what the rulebook's imports made.
func deleteRulebookObjects(c *Ctx, obj Obj) error {
	id, _ := api.ID(obj["id"])
	ic := importCtx(c, id)
	for _, name := range []string{"event_rules", "event_connectors", "event_streams"} {
		res := api.Lookup(name)
		objs, err := importedObjects(ic, res, id)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if err := api.Delete(ic, res, o); err != nil {
				return err
			}
		}
	}
	return nil
}

func importCtx(c *Ctx, rulebook int64) *Ctx {
	return &Ctx{Context: c.Context, App: c.App, User: rbac.System(), Tx: c.Tx, Extra: map[string]any{"rulebook": rulebook}}
}

// importedObjects are a rulebook's objects of a resource, by key.
func importedObjects(c *Ctx, res *Resource, rulebook int64) (map[string]Obj, error) {
	rows, err := c.DB().Query(c, "SELECT id, rulebook_key FROM "+res.Table+" WHERE rulebook_id = $1 ORDER BY id", rulebook)
	if err != nil {
		return nil, err
	}
	type row struct {
		id  int64
		key string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.key)
	})
	if err != nil {
		return nil, err
	}
	out := map[string]Obj{}
	for _, x := range list {
		obj, err := api.Get(c, res, x.id)
		if err != nil {
			return nil, err
		}
		if _, dup := out[x.key]; dup {
			x.key = fmt.Sprintf("%s#%d", x.key, x.id)
		}
		out[x.key] = obj
	}
	return out, nil
}

// savepoint runs fn in a savepoint of c's transaction: an object the
// import can't save leaves the others.
func savepoint(c *Ctx, fn func(c *Ctx) error) error {
	tx, ok := c.Tx.(pgx.Tx)
	if !ok {
		return errors.New("an import runs in a transaction")
	}
	sp, err := tx.Begin(c)
	if err != nil {
		return err
	}
	defer sp.Rollback(context.Background())
	sc := *c
	sc.Tx = sp
	if err := fn(&sc); err != nil {
		return err
	}
	return sp.Commit(c)
}

// ImportRulebook translates a rulebook again and applies its plan, in c's
// transaction. viewer is who the import looks templates up as: a template
// they may not see is reported missing.
func ImportRulebook(c *Ctx, id int64, viewer *rbac.Principal) error {
	ic := importCtx(c, id)
	rb, err := api.Get(ic, api.Lookup("rulebooks"), id)
	if err != nil {
		return err
	}
	pname := ""
	if err := c.DB().QueryRow(c, "SELECT name FROM unified_job_templates WHERE id = $1", rb["project"]).Scan(&pname); err != nil {
		return err
	}
	vars, _ := rb["variables"].(map[string]any)
	plan, err := rulebooks.Translate([]byte(str(rb["rulesets"])), rulebooks.Options{Name: pname + "/" + str(rb["name"]), Variables: vars})
	if err != nil {
		_, err := c.DB().Exec(c, "UPDATE rulebooks SET import_error = $2, imported = now(), modified = now() WHERE id = $1", id, err.Error()+".")
		return err
	}
	org, _ := api.ID(rb["organization"])
	a := &applier{c: ic, id: id, org: org, plan: plan, viewer: viewer, streams: map[string]int64{}}
	if err := a.apply(); err != nil {
		return err
	}
	plan.Report.Finish()
	_, err = c.DB().Exec(c, "UPDATE rulebooks SET report = $2, import_error = '', imported = now(), modified = now() WHERE id = $1", id, plan.Report)
	return err
}

type applier struct {
	c       *Ctx
	id, org int64
	plan    *rulebooks.Plan
	viewer  *rbac.Principal
	streams map[string]int64
}

func (a *applier) apply() error {
	rep := a.plan.Report
	streams := api.Lookup("event_streams")
	have, err := importedObjects(a.c, streams, a.id)
	if err != nil {
		return err
	}
	failed := map[string]string{}
	for _, s := range a.plan.Streams {
		obj, err := a.save(streams, have[s.Key], s.Key, Obj{"name": s.Name, "organization": a.org, "stream_type": s.Type}, nil)
		if err != nil {
			failed[s.Key] = "Its event stream can't be saved: " + describeError(err)
			continue
		}
		a.streams[s.Key], _ = api.ID(obj["id"])
		if rs := rulesetReport(rep, s.Ruleset); rs != nil {
			rs.Streams = append(rs.Streams, objectRef("event_stream", obj))
		}
		if src := sourceByKey(rep, s.Key); src != nil {
			ref := objectRef("event_stream", obj)
			src.Object = &ref
		}
	}
	connectors := api.Lookup("event_connectors")
	haveConnectors, err := importedObjects(a.c, connectors, a.id)
	if err != nil {
		return err
	}
	keepConnectors := map[string]bool{}
	for _, cn := range a.plan.Connectors {
		src := sourceByKey(rep, cn.Key)
		stream, ok := a.streams[cn.Stream]
		if !ok {
			if src != nil {
				src.Status, src.Reasons = rulebooks.NotTranslated, append(src.Reasons, failed[cn.Stream])
			}
			continue
		}
		filters := cn.Filters
		if filters == nil {
			filters = []any{}
		}
		data := Obj{"name": cn.Name, "organization": a.org, "event_stream": stream, "source": cn.Source, "args": cn.Args, "filters": filters}
		obj, err := a.save(connectors, haveConnectors[cn.Key], cn.Key, data, Obj{"enabled": false})
		if err != nil {
			if src != nil {
				src.Status, src.Reasons = rulebooks.NotTranslated, append(src.Reasons, "awxnext refused its connector: "+describeError(err))
			}
			continue
		}
		keepConnectors[cn.Key] = true
		if src != nil {
			ref := objectRef("event_connector", obj)
			src.Object = &ref
		}
	}
	rulesRes := api.Lookup("event_rules")
	haveRules, err := importedObjects(a.c, rulesRes, a.id)
	if err != nil {
		return err
	}
	keepRules := map[string]bool{}
	for _, r := range a.plan.Rules {
		rr, ar := rep.Lookup(r.Ruleset, r.Rule, r.Action)
		fail := func(reason string) {
			if ar != nil {
				ar.Status, ar.As, ar.Reason = rulebooks.NotTranslated, "", reason
			}
		}
		data, err := a.ruleData(r, rr)
		if err != nil {
			fail(err.Error())
			continue
		}
		obj, err := a.save(rulesRes, haveRules[r.Key], r.Key, data, Obj{"enabled": false, "mode": modeShadow, "description": r.Description})
		if err != nil {
			fail("awxnext refused the rule: " + describeError(err))
			continue
		}
		keepRules[r.Key] = true
		if ar != nil {
			ref := objectRef("event_rule", obj)
			ar.Object = &ref
		}
	}
	settleLogged(rep)
	for key, obj := range haveRules {
		if !keepRules[key] {
			if err := api.Delete(a.c, rulesRes, obj); err != nil {
				return err
			}
		}
	}
	for key, obj := range haveConnectors {
		if !keepConnectors[key] {
			if err := api.Delete(a.c, connectors, obj); err != nil {
				return err
			}
		}
	}
	for key, obj := range have {
		if _, ok := a.streams[key]; !ok {
			if err := api.Delete(a.c, streams, obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// save creates or updates an imported object in a savepoint. created are
// the values a new object starts with, which an operator then decides.
func (a *applier) save(res *Resource, old Obj, key string, data, created Obj) (Obj, error) {
	var obj Obj
	err := savepoint(a.c, func(c *Ctx) error {
		var err error
		if old == nil {
			for k, v := range created {
				data[k] = v
			}
			obj, err = api.Create(c, res, data, Obj{"rulebook": a.id, "rulebook_key": key})
			return err
		}
		changed := Obj{}
		for k, v := range data {
			if !sameValue(old[k], v) {
				changed[k] = v
			}
		}
		if len(changed) == 0 {
			obj = old
			return nil
		}
		obj, err = api.Update(c, res, old, changed)
		return err
	})
	return obj, err
}

// ruleData is a planned rule as event_rules takes it: its template looked
// up, and what the template doesn't prompt for left out, as AWX leaves it
// out of a launch from ansible-rulebook.
func (a *applier) ruleData(r rulebooks.Rule, rr *rulebooks.RuleReport) (Obj, error) {
	var streams []int64
	for _, k := range r.Streams {
		id, ok := a.streams[k]
		if !ok {
			return nil, errors.New("Its event stream can't be saved.")
		}
		streams = append(streams, id)
	}
	data := Obj{"name": r.Name, "organization": a.org, "streams": streams,
		"filter": r.Filter, "key": r.EventKey, "throttle": r.Throttle, "throttle_window": r.ThrottleWindow,
		"pattern": r.Pattern, "conditions": toAnyList(r.Conditions), "pattern_count": r.PatternCount, "pattern_window": r.PatternWindow,
		"action": r.Kind, "unified_job_template": nil, "extra_vars": Obj{}, "limit": "", "target_stream": nil,
		"event_type": r.EventType, "event_data": r.EventData, "message": r.Message,
		"retries": r.Retries, "retry_delay": r.RetryDelay, "post_artifacts": r.PostArtifacts, "artifacts_stream": nil}
	if r.TargetStream != "" {
		id, ok := a.streams[r.TargetStream]
		if !ok {
			return nil, errors.New("The stream it posts to can't be saved.")
		}
		data["target_stream"] = id
	}
	if r.ArtifactsStream != "" {
		id, ok := a.streams[r.ArtifactsStream]
		if !ok {
			return nil, errors.New("The stream it posts the artifacts to can't be saved.")
		}
		data["artifacts_stream"] = id
	}
	if r.Template == nil {
		return data, nil
	}
	tpl, askVars, askLimit, err := a.template(r.Template)
	if err != nil {
		return nil, err
	}
	data["unified_job_template"] = tpl
	vars := Obj{}
	for k, v := range r.ExtraVars {
		vars[k] = v
	}
	if r.EDAVars != "" {
		vars["ansible_eda"] = r.EDAVars
	}
	if len(vars) > 0 {
		known := map[string]bool{}
		if !askVars {
			known = surveyVariables(templateSurvey(a.c, tpl))
		}
		var dropped []string
		for k := range vars {
			if !askVars && !known[k] {
				dropped = append(dropped, k)
				delete(vars, k)
			}
		}
		if len(dropped) > 0 {
			sort.Strings(dropped)
			note := fmt.Sprintf("The template doesn't prompt for %s: AWX leaves them out of the launch from ansible-rulebook, and so does the rule. Turn on the template's Prompt on launch for variables, and import the rulebook again.",
				strings.Join(dropped, ", "))
			addNote(rr, note)
		}
	}
	data["extra_vars"] = vars
	if !askLimit {
		if r.LimitSet {
			addNote(rr, "The template doesn't prompt for a limit: AWX leaves the action's limit out, and so does the rule. Turn on the template's Prompt on launch for the limit, and import the rulebook again.")
		}
	} else {
		data["limit"] = r.Limit
	}
	return data, nil
}

func addNote(rr *rulebooks.RuleReport, note string) {
	if rr != nil && !slices.Contains(rr.Notes, note) {
		rr.Notes = append(rr.Notes, note)
	}
}

// template finds the template an action names, by its name and its
// organization's, among those the viewer may see.
func (a *applier) template(t *rulebooks.Template) (int64, bool, bool, error) {
	what := "job template"
	if t.Type == "workflow_job_template" {
		what = "workflow template"
	}
	missing := fmt.Errorf("No %s %s in organization %s: create it, then import the rulebook again.", what, t.Name, t.Organization)
	rows, err := a.c.DB().Query(a.c, `SELECT t.id, t.ask_variables_on_launch, t.ask_limit_on_launch FROM unified_job_templates t
		JOIN organizations o ON o.id = t.organization_id WHERE t.type = $1 AND t.name = $2 AND o.name = $3 ORDER BY t.id`, t.Type, t.Name, t.Organization)
	if err != nil {
		return 0, false, false, err
	}
	type found struct {
		id                int64
		askVars, askLimit bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
		var f found
		return f, r.Scan(&f.id, &f.askVars, &f.askLimit)
	})
	if err != nil {
		return 0, false, false, err
	}
	for _, f := range list {
		if a.viewer != nil && !a.viewer.Superuser {
			vc := *a.c
			vc.User = a.viewer
			ok, err := api.CanView(&vc, api.Lookup(t.Type+"s"), f.id)
			if err != nil {
				return 0, false, false, err
			}
			if !ok {
				continue
			}
		}
		return f.id, f.askVars, f.askLimit, nil
	}
	return 0, false, false, missing
}

// settleLogged marks the debug and print_event actions of rules that made
// no rule as not translated: no decision records them.
func settleLogged(rep *rulebooks.Report) {
	for _, rs := range rep.Rulesets {
		for _, rr := range rs.Rules {
			made := slices.ContainsFunc(rr.Actions, func(a *rulebooks.ActionReport) bool { return a.Object != nil })
			for _, ar := range rr.Actions {
				if ar.As == "decisions" && !made {
					ar.Status, ar.As, ar.Reason = rulebooks.NotTranslated, "", "No rule of its is saved: no decision records it."
				}
			}
		}
	}
}

func objectRef(typ string, obj Obj) rulebooks.Object {
	id, _ := api.ID(obj["id"])
	return rulebooks.Object{Type: typ, ID: id, Name: str(obj["name"])}
}

func rulesetReport(rep *rulebooks.Report, name string) *rulebooks.RulesetReport {
	for _, rs := range rep.Rulesets {
		if rs.Name == name {
			return rs
		}
	}
	return nil
}

func sourceByKey(rep *rulebooks.Report, key string) *rulebooks.SourceReport {
	for _, rs := range rep.Rulesets {
		for _, s := range rs.Sources {
			if s.Key == key {
				return s
			}
		}
	}
	return nil
}

func importProjectRulebooksView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	dir, rev, err := projectCheckout(c, obj)
	if err != nil {
		api.WriteError(w, api.BadRequest(err.Error()))
		return
	}
	if _, err := ImportProjectRulebooks(c, c.App, id, dir, rev, c.User); err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup("rulebooks")
	list, _, err := api.List(c, res, api.ListParams{Params: url.Values{"project": {strconv.FormatInt(id, 10)}}, Page: 1, PageSize: maxProjectRulebooks})
	if err == nil {
		list, err = api.Serialize(c, res, list)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"count": len(list), "results": list})
}

// projectCheckout is the directory of the project's files on this node:
// the manual project's, or the git project's last sync.
func projectCheckout(c *Ctx, obj Obj) (string, string, error) {
	lp := str(obj["local_path"])
	if lp == "" || strings.Contains(lp, "..") || strings.ContainsRune(lp, '/') {
		return "", "", errors.New("The project has no local path.")
	}
	dir := filepath.Join(c.App.Cfg.ProjectsRoot, lp)
	if _, err := os.Stat(dir); err != nil {
		return "", "", errors.New("The project's files aren't on this node: sync the project.")
	}
	return dir, str(obj["scm_revision"]), nil
}

// projectRulebook is a rulebook file of a project.
type projectRulebook struct {
	name, path, text string
}

// scanRulebooks lists the rulebooks of a project's directory: the YAML
// files of RulebookDirs that are lists of rulesets, regular files within
// the directory only.
func scanRulebooks(dir string) ([]projectRulebook, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []projectRulebook
	names := map[string]bool{}
	for _, d := range RulebookDirs {
		entries, err := readRootDir(root, d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".yml" && ext != ".yaml" || !e.Type().IsRegular() {
				continue
			}
			rel := d + "/" + e.Name()
			info, err := root.Lstat(rel)
			if err != nil || !info.Mode().IsRegular() || info.Size() > rulebooks.MaxSize {
				continue
			}
			text, err := readRootFile(root, rel)
			if err != nil || !rulebookShaped(text) {
				continue
			}
			name := e.Name()
			if names[name] {
				name = rel
			}
			names[name] = true
			out = append(out, projectRulebook{name: name, path: rel, text: string(text)})
			if len(out) >= maxProjectRulebooks {
				return out, nil
			}
		}
	}
	return out, nil
}

func readRootDir(root *os.Root, dir string) ([]os.DirEntry, error) {
	f, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

func readRootFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, rulebooks.MaxSize+1))
}

// rulebookShaped says whether a YAML file is a rulebook: a list of
// mappings with rules or sources. A file that isn't YAML is one, to report.
func rulebookShaped(text []byte) bool {
	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return true
	}
	list, ok := doc.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := m["rules"]; !ok {
			if _, ok := m["sources"]; !ok {
				return false
			}
		}
	}
	return true
}

// ImportProjectRulebooks imports the rulebooks of a project's files at
// revision: new files become rulebooks, changed ones change, all are
// translated again, and those whose file is gone are deleted with their
// objects. viewer is who looks templates up; nil: the project's last
// editor.
func ImportProjectRulebooks(c *Ctx, app *api.App, project int64, dir, revision string, viewer *rbac.Principal) (int, error) {
	files, err := scanRulebooks(dir)
	if err != nil {
		return 0, err
	}
	sys := &Ctx{Context: c.Context, App: app, User: rbac.System(), Tx: c.Tx, Extra: map[string]any{"rulebook": int64(0)}}
	if viewer == nil {
		var uid *int64
		if err := sys.DB().QueryRow(c, "SELECT coalesce(modified_by_id, created_by_id) FROM unified_job_templates WHERE id = $1", project).Scan(&uid); err != nil {
			return 0, err
		}
		if uid != nil {
			if viewer, err = auth.LoadPrincipal(c, app, *uid); err != nil {
				return 0, err
			}
		}
	}
	return len(files), sys.WithTx(func(sys *Ctx) error {
		res := api.Lookup("rulebooks")
		rows, err := sys.DB().Query(sys, "SELECT id, path FROM rulebooks WHERE project_id = $1 AND path <> '' ORDER BY id", project)
		if err != nil {
			return err
		}
		have := map[string]int64{}
		for rows.Next() {
			var id int64
			var path string
			if err := rows.Scan(&id, &path); err != nil {
				rows.Close()
				return err
			}
			have[path] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, f := range files {
			seen[f.path] = true
			id, ok := have[f.path]
			if ok {
				if _, err := sys.DB().Exec(sys, "UPDATE rulebooks SET rulesets = $2, revision = $3, modified = now() WHERE id = $1", id, f.text, revision); err != nil {
					return err
				}
			} else {
				name := f.name
				var taken bool
				if err := sys.DB().QueryRow(sys, "SELECT EXISTS (SELECT 1 FROM rulebooks WHERE project_id = $1 AND name = $2)", project, name).Scan(&taken); err != nil {
					return err
				}
				if taken {
					name = f.path
				}
				obj, err := api.Create(sys, res, Obj{"name": name, "project": project, "rulesets": f.text},
					Obj{"path": f.path, "revision": revision})
				if err != nil {
					return err
				}
				id, _ = api.ID(obj["id"])
			}
			if err := ImportRulebook(sys, id, viewer); err != nil {
				return err
			}
		}
		for path, id := range have {
			if seen[path] {
				continue
			}
			obj, err := api.Get(sys, res, id)
			if err != nil {
				return err
			}
			if err := api.Delete(importCtx(sys, id), res, obj); err != nil {
				return err
			}
		}
		return nil
	})
}

// RulebooksProjectUpdated imports a project's rulebooks once a sync
// recorded its new revision: its files are those of dir. It returns how
// many it found.
func RulebooksProjectUpdated(ctx context.Context, app *api.App, project int64, dir, revision string) (int, error) {
	c := &Ctx{Context: ctx, App: app, User: rbac.System()}
	n, err := ImportProjectRulebooks(c, app, project, dir, revision, nil)
	if err != nil {
		return n, fmt.Errorf("importing the project's rulebooks: %w", err)
	}
	return n, nil
}

package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/expr-lang/expr"
)

// Catalog items (awxnext, roadmap M15-01): a job or workflow template
// published for people who request it without any role on the template.
// The item's form is a view over the template's survey: it rewords, hides
// and narrows questions, sets defaults and fixes answers, and adds no
// variable. A request runs the template with the item's answers, the
// requester as its launcher; the request's jobs are not the requester's to
// see, since they hold the item's hidden answers.

const (
	itemRunOnce    = "run_once"
	itemDeployment = "deployment"
	maxLifetime    = 3650

	requestPending    = "pending"
	requestRunning    = "running"
	requestSuccessful = "successful"
	requestFailed     = "failed"
	requestCanceled   = "canceled"
)

// itemQuestionKeys are what an item may say of a survey question.
var itemQuestionKeys = map[string]bool{"question_name": true, "question_description": true, "choices": true, "default": true,
	"filter": true, "hidden": true, "value": true, "visible": true}

func optionalBetween(min, max int64) func(c *Ctx, v any, data Obj) error {
	return func(c *Ctx, v any, data Obj) error {
		if v == nil {
			return nil
		}
		return between(min, max)(c, v, data)
	}
}

func registerCatalogItems() {
	followTemplateOrg("job_templates")
	followTemplateOrg("workflow_job_templates")
	api.Register(&Resource{
		Name:         "catalog_items",
		Type:         "catalog_item",
		Table:        "catalog_items",
		Verbose:      "Catalog Item",
		Ordering:     "name",
		ContentType:  "catalogitem",
		OrgColumn:    "organization_id",
		Versioned:    true,
		Managed:      true,
		HistoryPerm:  "change",
		SearchFields: []string{"name", "description", "category"},
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Help: "The template's organization."},
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", Required: true,
				Help: "The job or workflow template the item publishes."},
			{Name: "kind", Type: api.TChoice, Default: itemRunOnce, Choices: labelled(itemRunOnce, "Runs once", itemDeployment, "Creates a deployment"),
				Help: "Whether a request runs once, such as a firewall rule, or creates a deployment that its owners keep."},
			{Name: "icon", Type: api.TString, Default: "", MaxLength: 64, Help: "The icon the portal shows, by name, such as server or database."},
			{Name: "category", Type: api.TString, Default: "", MaxLength: 128, Help: "The portal lists items by category."},
			{Name: "owner_team", Type: api.TFK, Ref: "teams", Nullable: true, Help: "The team of the item's organization that owns it."},
			{Name: "questions", Type: api.TJSON, Default: Obj{}, Redact: redactItemQuestions,
				Help: "What the item makes of each question of the template's survey, by variable: question_name and question_description reword it, choices narrows its fixed choices, filter narrows a dynamic list with an expression, default sets its default, value fixes the answer (shown to requesters if visible), and hidden leaves it to its default. Only the item's admins see it."},
			{Name: "max_lifetime_days", Type: api.TInt, Nullable: true, Validate: optionalBetween(1, maxLifetime),
				Help: "For an item that creates a deployment, the longest a requester may keep it, in days. Empty: they may keep it for good."},
			{Name: "default_lifetime_days", Type: api.TInt, Nullable: true, Validate: optionalBetween(1, maxLifetime),
				Help: "For an item that creates a deployment, how long the form proposes to keep it, in days."},
		},
		CanAdd:     func(c *Ctx, data Obj) error { return requireUJTPerm(c, data["unified_job_template"], "change") },
		CanChange:  changeCatalogItem,
		BeforeSave: saveCatalogItem,
		Serialize: func(c *Ctx, o Obj) {
			if ok, _ := api.HasPerm(c, api.Lookup("catalog_items"), o, "change"); !ok {
				delete(o, "questions")
			} else {
				o["questions"] = redactItemQuestions(o["questions"])
			}
			if rel, ok := o["related"].(Obj); ok {
				base := api.Lookup("catalog_items").URL(o["id"])
				rel["request"] = base + "request/"
				rel["survey_choices"] = base + "survey_choices/"
			}
		},
		SubLists: []*SubList{readOnlyChildSub("requests", "catalog_requests", "catalog_item_id")},
		Actions: []*api.Action{
			{Name: "request", Methods: []string{"GET", "POST"}, Perm: "request", Handler: handleCatalogRequest},
			{Name: "survey_choices", Methods: []string{"GET"}, Perm: "request", Handler: handleItemChoices},
		},
	})

	api.Register(&Resource{
		Name:         "catalog_requests",
		Type:         "catalog_request",
		Table:        "catalog_requests",
		Verbose:      "Catalog Request",
		NoName:       true,
		NoCreate:     true,
		NoUpdate:     true,
		NoDelete:     true,
		Ordering:     "-id",
		SearchFields: []string{"item_name", "reason"},
		Fields: []*Field{
			{Name: "catalog_item", Type: api.TFK, Ref: "catalog_items", ReadOnly: true, Nullable: true},
			{Name: "item_name", Type: api.TString, ReadOnly: true, Help: "The item's name when it was requested."},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true},
			{Name: "team", Type: api.TFK, Ref: "teams", ReadOnly: true, Nullable: true, Help: "The team the request is for."},
			{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: labelled(requestPending, "Pending", requestRunning, "Running",
				requestSuccessful, "Successful", requestFailed, "Failed", requestCanceled, "Canceled")},
			{Name: "answers", Type: api.TJSON, ReadOnly: true, Help: "What the requester answered, passwords masked, and the fixed answers they were shown."},
			{Name: "answer_labels", Type: api.TJSON, ReadOnly: true, Help: "What the requester saw for the answers picked in labelled lists, such as a choice source's, as they were when the request was made."},
			{Name: "reason", Type: api.TText, ReadOnly: true, Help: "Why the requester needs it."},
			{Name: "lifetime_days", Type: api.TInt, ReadOnly: true, Nullable: true, Help: "How long to keep the deployment, in days. Empty: for good, or an item that runs once."},
			{Name: "job", Column: "unified_job_id", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true,
				Help: "The job or workflow job that runs the request. Its details are for those who can see the job."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			a := rbac.ArgFunc(arg)
			admin := c.User.Predicate(a, "catalogitem", "change_catalogitem", rbac.Scope{ID: "t.catalog_item_id", Org: "t.organization_id"})
			audit := c.User.Predicate(a, "organization", "audit_organization", rbac.Scope{ID: "t.organization_id", Org: "t.organization_id"})
			return fmt.Sprintf("t.created_by_id = %s OR %s OR %s", arg(c.User.UserID), admin, audit)
		},
	})
}

// followTemplateOrg moves a template's items with it to another
// organization: requesters of the old one keep no access to them.
func followTemplateOrg(name string) {
	res := api.Lookup(name)
	prev := res.AfterUpdate
	res.AfterUpdate = func(c *Ctx, old, obj Obj) error {
		if prev != nil {
			if err := prev(c, old, obj); err != nil {
				return err
			}
		}
		org, ok := api.ID(obj["organization"])
		if !ok || sameValue(old["organization"], obj["organization"]) {
			return nil
		}
		_, err := c.DB().Exec(c, `UPDATE catalog_items i SET organization_id = $2, modified = now(),
			owner_team_id = (SELECT t.id FROM teams t WHERE t.id = i.owner_team_id AND t.organization_id = $2)
			WHERE unified_job_template_id = $1`, obj["id"], org)
		if err != nil && strings.Contains(err.Error(), "catalog_items_organization_id_name_key") {
			return api.FieldError("organization", "A catalog item of this template has the name of one in that organization: rename it first.")
		}
		return err
	}
}

// changeCatalogItem: the item's admins change it, and what runs (its
// template and its answers) only an Admin of the template too.
func changeCatalogItem(c *Ctx, old, merged Obj) error {
	ok, err := api.HasPerm(c, api.Lookup("catalog_items"), old, "change")
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	if !sameValue(old["unified_job_template"], merged["unified_job_template"]) ||
		!sameValue(redactItemQuestions(old["questions"]), redactItemQuestions(merged["questions"])) {
		return requireUJTPerm(c, merged["unified_job_template"], "change")
	}
	return nil
}

func saveCatalogItem(c *Ctx, old, data Obj) error {
	pick := func(k string) any {
		if v, ok := data[k]; ok {
			return v
		}
		return old[k]
	}
	ujt, ok := api.ID(pick("unified_job_template"))
	if !ok {
		return api.FieldError("unified_job_template", "This field is required.")
	}
	var typ string
	var org *int64
	if err := c.DB().QueryRow(c, "SELECT type, organization_id FROM unified_job_templates WHERE id = $1", ujt).Scan(&typ, &org); err != nil {
		return api.FieldError("unified_job_template", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", ujt))
	}
	if typ != "job_template" && typ != "workflow_job_template" {
		return api.FieldError("unified_job_template", "A catalog item publishes a job template or a workflow template.")
	}
	if org == nil {
		return api.FieldError("unified_job_template", "The template must belong to an organization.")
	}
	data["organization"] = *org
	if team, ok := api.ID(pick("owner_team")); ok {
		var torg int64
		if err := c.DB().QueryRow(c, "SELECT organization_id FROM teams WHERE id = $1", team).Scan(&torg); err != nil || torg != *org {
			return api.FieldError("owner_team", "The owner team must be of the template's organization.")
		}
	}
	if pick("kind") != itemDeployment && (pick("max_lifetime_days") != nil || pick("default_lifetime_days") != nil) {
		return api.FieldError("max_lifetime_days", "Only an item that creates a deployment has a lifetime.")
	}
	if def, ok := api.ID(pick("default_lifetime_days")); ok {
		if max, ok := api.ID(pick("max_lifetime_days")); ok && def > max {
			return api.FieldError("default_lifetime_days", "The default lifetime is longer than the longest.")
		}
	}
	_, given := data["questions"]
	if !given && old != nil && sameValue(old["unified_job_template"], ujt) {
		return nil
	}
	var prev map[string]any
	if old != nil {
		prev, _ = old["questions"].(map[string]any)
	}
	qs, err := checkItemQuestions(c, ujt, pick("questions"), prev)
	if err != nil {
		return err
	}
	data["questions"] = qs
	return nil
}

// enabledSurvey is the template's survey, or nothing when it is off.
func enabledSurvey(c *Ctx, ujt int64) map[string]any {
	var on bool
	c.DB().QueryRow(c, "SELECT survey_enabled FROM unified_job_templates WHERE id = $1", ujt).Scan(&on)
	if !on {
		return map[string]any{}
	}
	return templateSurvey(c, ujt)
}

// checkItemQuestions checks an item's questions against its template's
// survey and encrypts the password answers it fixes, keeping those sent
// back as $encrypted$.
func checkItemQuestions(c *Ctx, ujt int64, v any, prev map[string]any) (map[string]any, error) {
	bad := func(msg string) error { return api.FieldError("questions", msg) }
	qs, ok := v.(map[string]any)
	if v == nil {
		qs, ok = map[string]any{}, true
	}
	if !ok {
		return nil, bad("Expected an object of questions by variable.")
	}
	survey := map[string]map[string]any{}
	for _, q := range surveyQuestions(enabledSurvey(c, ujt)) {
		survey[str(q["variable"])] = q
	}
	out := map[string]any{}
	for variable, raw := range qs {
		ov, ok := raw.(map[string]any)
		if !ok {
			return nil, bad(fmt.Sprintf("%s: expected an object.", variable))
		}
		if survey[variable] == nil {
			return nil, bad(fmt.Sprintf("%s is not a question of the template's survey: an item adds no variable.", variable))
		}
		for k := range ov {
			if !itemQuestionKeys[k] {
				return nil, bad(fmt.Sprintf("%s: %s is not one of question_name, question_description, choices, filter, default, value, visible, hidden.", variable, k))
			}
		}
		ov = maps.Clone(ov)
		if survey[variable]["type"] == "password" {
			if _, ok := ov["default"]; ok {
				return nil, bad(fmt.Sprintf("%s: a password question's answer is fixed with value, not given a default.", variable))
			}
			if b, _ := ov["visible"].(bool); b {
				return nil, bad(fmt.Sprintf("%s: a password answer is never shown.", variable))
			}
			if s, isStr := ov["value"].(string); isStr {
				old, _ := prev[variable].(map[string]any)
				kept, _ := old["value"].(string)
				switch {
				case s == crypto.Placeholder || (crypto.IsEncrypted(s) && s == kept):
					if !crypto.IsEncrypted(kept) {
						return nil, bad(fmt.Sprintf("%s: $encrypted$ keeps a password the item already fixes.", variable))
					}
					ov["value"] = kept
				case crypto.IsEncrypted(s):
					return nil, bad(fmt.Sprintf("%s: give the password in clear, or $encrypted$ to keep the item's.", variable))
				case s != "":
					enc, err := c.App.Crypto.Encrypt(s)
					if err != nil {
						return nil, err
					}
					ov["value"] = enc
				}
			}
		}
		out[variable] = ov
	}
	view, err := newItemView(enabledSurvey(c, ujt), out)
	if err != nil {
		return nil, bad(err.Error())
	}
	if err := view.checkAnswers(c); err != nil {
		return nil, bad(err.Error())
	}
	return out, nil
}

// itemView is an item's form: the template's survey with the item's
// changes, the fixed answers as the defaults of the questions they answer.
type itemView struct {
	spec    map[string]any
	asked   []map[string]any
	locked  map[string]bool
	visible map[string]bool
	filters map[string]string
}

func newItemView(survey map[string]any, overrides map[string]any) (*itemView, error) {
	v := &itemView{locked: map[string]bool{}, visible: map[string]bool{}, filters: map[string]string{}}
	var qs []any
	known, computed := map[string]bool{}, map[string]bool{}
	for _, q := range surveyQuestions(survey) {
		q = maps.Clone(q)
		variable := str(q["variable"])
		known[variable] = true
		if q["value_from"] != nil {
			computed[variable] = true
			qs = append(qs, q)
			continue
		}
		ov, _ := overrides[variable].(map[string]any)
		for _, k := range []string{"question_name", "question_description"} {
			if s, ok := ov[k]; ok {
				if _, isStr := s.(string); !isStr {
					return nil, fmt.Errorf("%s: %s must be text", variable, k)
				}
				q[k] = s
			}
		}
		if list, ok := ov["choices"]; ok {
			if q["choices_from"] != nil || (q["type"] != "multiplechoice" && q["type"] != "multiselect") {
				return nil, fmt.Errorf("%s: choices narrows the fixed choices of a multiplechoice or multiselect question", variable)
			}
			narrowed := surveyChoices(list)
			if _, isList := list.([]any); !isList || len(narrowed) == 0 {
				return nil, fmt.Errorf("%s: choices must be a list of the survey's choices", variable)
			}
			all := surveyChoices(q["choices"])
			for _, ch := range narrowed {
				if !contains(all, ch) {
					return nil, fmt.Errorf("%s: %q is not one of the survey's choices", variable, ch)
				}
			}
			q["choices"] = toAnyList(narrowed)
			if d, ok := q["default"]; ok && d != "" && d != nil && ov["default"] == nil {
				defs := surveyChoices(d)
				if q["type"] == "multiplechoice" {
					defs = []string{fmt.Sprint(d)}
				}
				kept := []string{}
				for _, x := range defs {
					if contains(narrowed, x) {
						kept = append(kept, x)
					}
				}
				q["default"] = strings.Join(kept, "\n")
			}
		}
		if f, ok := ov["filter"]; ok {
			s, _ := f.(string)
			if q["choices_from"] == nil || s == "" {
				return nil, fmt.Errorf("%s: filter narrows a dynamic list (choices_from) with an expression", variable)
			}
			if err := checkExpr(s); err != nil {
				return nil, fmt.Errorf("%s: filter: %v", variable, err)
			}
			v.filters[variable] = s
		}
		if d, ok := ov["default"]; ok {
			q["default"] = d
		}
		hidden, _ := ov["hidden"].(bool)
		if _, ok := ov["hidden"]; ok {
			if _, isBool := ov["hidden"].(bool); !isBool {
				return nil, fmt.Errorf("%s: hidden must be true or false", variable)
			}
		}
		fixed, isFixed := ov["value"]
		if vis, ok := ov["visible"]; ok {
			b, isBool := vis.(bool)
			if !isBool || (b && !isFixed) {
				return nil, fmt.Errorf("%s: visible shows a fixed answer (value) to requesters", variable)
			}
			v.visible[variable] = b
		}
		if isFixed {
			q["default"] = fixed
			v.locked[variable] = true
		} else if hidden {
			req, _ := q["required"].(bool)
			if d := q["default"]; req && (d == nil || d == "") && q["show_if"] == nil {
				return nil, fmt.Errorf("%s is required and has no default: fix its answer with value, or ask it", variable)
			}
			v.locked[variable] = true
		} else {
			v.asked = append(v.asked, q)
		}
		qs = append(qs, q)
	}
	for variable := range overrides {
		if computed[variable] {
			return nil, fmt.Errorf("%s is computed by the server (value_from): an item can't change it", variable)
		}
		if !known[variable] {
			return nil, fmt.Errorf("%s is not a question of the template's survey", variable)
		}
	}
	v.spec = map[string]any{}
	if len(qs) > 0 {
		v.spec = map[string]any{"name": survey["name"], "description": survey["description"], "spec": qs}
	}
	return v, nil
}

// checkAnswers checks the defaults and fixed answers of the questions
// with fixed choices, as a request would.
func (v *itemView) checkAnswers(c *Ctx) error {
	for _, q := range surveyQuestions(v.spec) {
		d, ok := q["default"]
		if !ok || d == nil || d == "" || q["choices_from"] != nil {
			continue
		}
		one := maps.Clone(q)
		delete(one, "show_if")
		one["required"] = true
		answer := map[string]any{}
		if q["type"] == "password" {
			if s, _ := d.(string); crypto.IsEncrypted(s) {
				continue
			}
		}
		if q["type"] == "multiselect" {
			if _, isList := d.([]any); !isList {
				answer[str(q["variable"])] = toAnyList(surveyChoices(d))
			}
		}
		if _, _, errs := ApplySurvey(c, map[string]any{"spec": []any{one}}, answer, SurveyContext{}); len(errs) > 0 {
			return errors.New(strings.Join(errs, " "))
		}
	}
	return nil
}

// filter narrows dynamic choices with the item's filter expressions, over
// each choice, the answers so far and the requester.
func (v *itemView) filter(c *Ctx) func(variable string, choices []string, answered map[string]any) ([]string, error) {
	user := userEnv(c, c.User.UserID)
	return func(variable string, choices []string, answered map[string]any) ([]string, error) {
		src := v.filters[variable]
		if src == "" {
			return choices, nil
		}
		prog, err := expr.Compile(src, expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("the item's filter: %v", err)
		}
		out := []string{}
		for _, ch := range choices {
			res, err := expr.Run(prog, map[string]any{"value": ch, "answers": answered, "user": user})
			if err != nil {
				return nil, fmt.Errorf("the item's filter: %v", err)
			}
			if truthy(res) {
				out = append(out, ch)
			}
		}
		return out, nil
	}
}

// redactItemQuestions masks the password answers an item fixes.
func redactItemQuestions(v any) any {
	qs, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, q := range qs {
		if m, ok := q.(map[string]any); ok {
			if s, _ := m["value"].(string); crypto.IsEncrypted(s) {
				m = maps.Clone(m)
				m["value"] = crypto.Placeholder
			}
			q = m
		}
		out[k] = q
	}
	return out
}

// catalogItem loads what a request of an item needs, as the system: the
// requester has no role on the template.
type catalogItem struct {
	item     Obj
	template Obj
	tplRes   *Resource
	view     *itemView
}

func loadCatalogItem(c *Ctx, item Obj) (*catalogItem, error) {
	ujt, _ := api.ID(item["unified_job_template"])
	sys := *c
	sys.User = rbac.System()
	res := api.Lookup(ujtType(c, ujt) + "s")
	if res == nil {
		return nil, api.Detail(400, "The catalog item's template no longer exists.")
	}
	tpl, err := api.Get(&sys, res, ujt)
	if err != nil {
		return nil, api.Detail(400, "The catalog item's template no longer exists.")
	}
	raw, _ := item["questions"].(map[string]any)
	view, err := newItemView(enabledSurvey(c, ujt), raw)
	if err != nil {
		return nil, api.Detail(400, fmt.Sprintf("The catalog item no longer matches its template's survey (%v): its owners must update it.", err))
	}
	return &catalogItem{item: item, template: tpl, tplRes: res, view: view}, nil
}

func (ci *catalogItem) surveyContext(c *Ctx) SurveyContext {
	inv, _ := api.ID(ci.template["inventory"])
	return SurveyContext{Template: ci.template, Inventory: inv, System: true, Filter: ci.view.filter(c)}
}

// requestTeams are the teams of the item's organization the requester is in.
func requestTeams(c *Ctx, org any) ([]Obj, error) {
	rows, err := c.DB().Query(c, "SELECT id, name FROM teams WHERE id = ANY($1) AND organization_id = $2 ORDER BY name, id", c.User.Teams, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obj{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, Obj{"id": id, "name": name})
	}
	return out, rows.Err()
}

// form is what the requester fills in: the questions the item asks, the
// fixed answers it shows, the teams to choose from and the lifetime.
func (ci *catalogItem) form(c *Ctx) (Obj, error) {
	questions := []Obj{}
	for _, q := range ci.view.asked {
		out := Obj{}
		for k, v := range q {
			if k != "choices_from" {
				out[k] = v
			}
		}
		out["dynamic"] = q["choices_from"] != nil
		if q["type"] == "password" {
			if s, _ := q["default"].(string); s != "" {
				out["default"] = crypto.Placeholder
			}
		}
		questions = append(questions, out)
	}
	fixed := []Obj{}
	for _, q := range surveyQuestions(ci.view.spec) {
		variable := str(q["variable"])
		if ci.view.visible[variable] && q["type"] != "password" {
			fixed = append(fixed, Obj{"variable": variable, "question_name": q["question_name"], "value": q["default"]})
		}
	}
	teams, err := requestTeams(c, ci.item["organization"])
	if err != nil {
		return nil, err
	}
	out := Obj{
		"catalog_item":  Obj{"id": ci.item["id"], "name": ci.item["name"], "description": ci.item["description"], "kind": ci.item["kind"], "icon": ci.item["icon"], "category": ci.item["category"]},
		"questions":     questions,
		"fixed_answers": fixed,
		"teams":         teams,
		"team_required": len(teams) > 1,
		"lifetime":      nil,
	}
	if ci.item["kind"] == itemDeployment {
		out["lifetime"] = Obj{"max_days": ci.item["max_lifetime_days"], "default_days": ci.item["default_lifetime_days"]}
	}
	return out, nil
}

var requestKeys = map[string]bool{"answers": true, "team": true, "reason": true, "lifetime_days": true}

func handleCatalogRequest(c *Ctx, w http.ResponseWriter, r *http.Request, item Obj) {
	ci, err := loadCatalogItem(c, item)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if r.Method == "GET" {
		form, err := ci.form(c)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, form)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	req, err := RequestCatalogItem(c, ci, normalizeObj(body))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out, err := api.SerializeOne(c, api.Lookup("catalog_requests"), req)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, out)
}

// RequestCatalogItem checks a request against the item's form and runs it.
func RequestCatalogItem(c *Ctx, ci *catalogItem, body Obj) (Obj, error) {
	fe := api.FieldErrors{}
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !requestKeys[k] {
			fe.Add(k, "Not a field of a request: answers, team, reason, lifetime_days.")
		}
	}
	answers := map[string]any{}
	if v, ok := body["answers"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			fe.Add("answers", "Expected an object of answers by variable.")
		}
		answers = m
	}
	known := surveyVariables(ci.view.spec)
	var names []string
	for k := range answers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		switch {
		case !known[k]:
			fe.Add("answers", fmt.Sprintf("%s is not a question of this item.", k))
		case ci.view.locked[k]:
			fe.Add("answers", fmt.Sprintf("%s is set by the item and can't be changed.", k))
		}
	}
	reason, isStr := body["reason"].(string)
	if body["reason"] != nil && !isStr {
		fe.Add("reason", "Not a valid string.")
	}
	team, err := requestTeam(c, ci.item["organization"], body["team"])
	if err != nil {
		fe.Add("team", err.Error())
	}
	lifetime, err := requestLifetime(ci.item, body)
	if err != nil {
		fe.Add("lifetime_days", err.Error())
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	var id int64
	if err := c.DB().QueryRow(c, "SELECT nextval(pg_get_serial_sequence('catalog_requests', 'id'))").Scan(&id); err != nil {
		return nil, err
	}
	created := time.Now().UTC().Truncate(time.Microsecond)
	sc := ci.surveyContext(c)
	sc.Labels = map[string]any{}
	sc.Request, sc.Team = requestEnv(id, created, lifetime), teamEnv(c, team)
	sv, pw, errs := ApplySurvey(c, ci.view.spec, answers, sc)
	if len(errs) > 0 {
		return nil, &api.Error{Status: 400, Body: Obj{"variables_needed_to_start": errs}}
	}
	shown := map[string]any{}
	keep := map[string]bool{}
	for k, v := range answers {
		if _, ok := sv[k]; ok {
			shown[k], keep[k] = v, true
			if contains(pw, k) {
				shown[k] = crypto.Placeholder
			}
		}
	}
	for k, vis := range ci.view.visible {
		if vis {
			if v, ok := sv[k]; ok {
				shown[k], keep[k] = v, true
			}
		}
	}
	labels := map[string]any{}
	for k, l := range sc.Labels {
		if keep[k] {
			labels[k] = l
		}
	}
	answersJSON, _ := json.Marshal(shown)
	labelsJSON, _ := json.Marshal(labels)
	var req Obj
	err = c.WithTx(func(c *Ctx) error {
		if _, err := c.DB().Exec(c, `INSERT INTO catalog_requests (id, created, modified, created_by_id, modified_by_id, catalog_item_id, item_name, organization_id, team_id,
			answers, answer_labels, reason, lifetime_days) VALUES ($1, $2, $2, $3, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			id, created, nullID(c.User.UserID), ci.item["id"], ci.item["name"], ci.item["organization"], team, string(answersJSON), string(labelsJSON), reason, lifetime); err != nil {
			return err
		}
		lc := *c
		lc.Extra = maps.Clone(c.Extra)
		if lc.Extra == nil {
			lc.Extra = map[string]any{}
		}
		lc.Extra["activity_vars"] = keep
		ov := &LaunchOverrides{Survey: &SurveyAnswers{Vars: sv, Passwords: pw}, ExtraJob: map[string]any{"catalog_request_id": id}}
		var jobID int64
		var err error
		if ci.tplRes.Name == "workflow_job_templates" {
			jobID, _, err = LaunchWorkflowJobTemplate(&lc, ci.template, Obj{}, "manual", ov)
		} else {
			jobID, _, err = LaunchJobTemplate(&lc, ci.template, Obj{}, "manual", ov)
		}
		if err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE catalog_requests SET unified_job_id = $2 WHERE id = $1", id, jobID); err != nil {
			return err
		}
		res := api.Lookup("catalog_requests")
		if req, err = api.Get(c, res, id); err != nil {
			return err
		}
		return api.Activity(c, "create", res, req, api.ActivityFields(res, req), nil, nil, "")
	})
	return req, err
}

// requestTeam is the team a request is for: the one given, which must be
// one of the requester's in the item's organization, or their only one.
func requestTeam(c *Ctx, org any, given any) (any, error) {
	teams, err := requestTeams(c, org)
	if err != nil {
		return nil, err
	}
	if given == nil {
		switch len(teams) {
		case 0:
			return nil, nil
		case 1:
			return teams[0]["id"], nil
		}
		return nil, errors.New("You are in several teams of the item's organization: say which team the request is for.")
	}
	id, ok := api.ID(given)
	for _, t := range teams {
		if ok && t["id"] == id {
			return id, nil
		}
	}
	return nil, errors.New("Not one of your teams in the item's organization.")
}

// requestLifetime is how long the request's deployment is kept: as asked,
// within the item's longest, or the item's default.
func requestLifetime(item Obj, body Obj) (any, error) {
	v, given := body["lifetime_days"]
	if item["kind"] != itemDeployment {
		if given && v != nil {
			return nil, errors.New("This item runs once and keeps nothing.")
		}
		return nil, nil
	}
	max, hasMax := api.ID(item["max_lifetime_days"])
	if !given || v == nil {
		if def, ok := api.ID(item["default_lifetime_days"]); ok {
			return def, nil
		}
		if hasMax {
			return nil, errors.New("Say how long to keep it.")
		}
		return nil, nil
	}
	days, ok := v.(int64)
	if !ok || days < 1 {
		return nil, errors.New("A number of days, at least 1.")
	}
	if hasMax && days > max {
		return nil, fmt.Errorf("At most %d days.", max)
	}
	if days > maxLifetime {
		return nil, fmt.Errorf("At most %d days.", maxLifetime)
	}
	return days, nil
}

// handleItemChoices serves /catalog_items/<id>/survey_choices/?variable=x
// with the choices of a dynamic question that the item asks, narrowed by
// its filter; answers (JSON) are the earlier answers the filter and a
// choice source may use, with the item's fixed answers in place of any
// given for them.
func handleItemChoices(c *Ctx, w http.ResponseWriter, r *http.Request, item Obj) {
	ci, err := loadCatalogItem(c, item)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	variable := r.URL.Query().Get("variable")
	given, err := answersParam(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	for _, q := range surveyQuestions(ci.view.spec) {
		if v := str(q["variable"]); ci.view.locked[v] {
			given[v] = q["default"]
		}
	}
	for _, q := range ci.view.asked {
		if str(q["variable"]) != variable {
			continue
		}
		opts, err := ci.surveyContext(c).options(c, q, earlierAnswers(ci.view.spec, variable, given))
		if err != nil {
			api.WriteJSON(w, 200, Obj{"variable": variable, "choices": []string{}, "error": err.Error()})
			return
		}
		api.WriteJSON(w, 200, choicesReply(variable, opts))
		return
	}
	api.WriteError(w, api.Detail(404, fmt.Sprintf("The item asks no question for %q.", variable)))
}

// CatalogJobStarted and CatalogJobFinished keep a request's status with
// its job's.
func CatalogJobStarted(ctx context.Context, m *jobs.Manager, job jobs.Row) {
	setRequestStatus(ctx, m, job, requestRunning)
}

func CatalogJobFinished(ctx context.Context, m *jobs.Manager, job jobs.Row) {
	status := requestFailed
	switch job.Str("status") {
	case "successful":
		status = requestSuccessful
	case "canceled":
		status = requestCanceled
	}
	setRequestStatus(ctx, m, job, status)
}

func setRequestStatus(ctx context.Context, m *jobs.Manager, job jobs.Row, status string) {
	if !job.Has("catalog_request_id") {
		return
	}
	m.DB().Exec(ctx, "UPDATE catalog_requests SET status = $3, modified = now() WHERE id = $1 AND unified_job_id = $2",
		job.Int("catalog_request_id"), job.ID(), status)
}

package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// Survey questions of type multiplechoice or multiselect can take their
// choices from the system instead of a fixed list ("choices_from"):
//
//	{"type": "hosts"}                          host names of the launch inventory
//	{"type": "groups", "inventory": 3}         group names of inventory 3
//	{"type": "git_refs", "refs": "tags"}       tags (newest first), "branches" or "all" of the template's project
//	{"type": "resource", "resource": "inventories", "field": "name", "filter": {"organization": 1}}
//	{"type": "source", "source": 4}            the list of choice source 4
//
// Choices are listed as the launching user, so they only see what they can
// view, and answers are checked against the same list at launch. A choice
// source's list is the source's, whoever launches: the survey's owners
// chose it, as they choose a template's credentials.

// SurveyContext is what dynamic survey choices are resolved against.
type SurveyContext struct {
	// Template is the template being launched.
	Template Obj
	// Inventory is the inventory the job will use (0 if none).
	Inventory int64
	// System lists dynamic choices as the system rather than as the user:
	// a catalog item's requester has no role on what its template uses,
	// and the item's publisher chose what its form lists.
	System bool
	// Filter, when set, narrows a dynamic list with the answers so far.
	Filter func(variable string, choices []string, answered map[string]any) ([]string, error)
	// Labels, when not nil, gets the labels of the answers picked in
	// labelled lists, by variable: a string, or a list for a multiselect.
	Labels map[string]any
}

// options lists a question's choices and their labels, narrowed by the
// context's filter. answered are the earlier answers.
func (sc SurveyContext) options(c *Ctx, q map[string]any, answered map[string]any) ([]choiceOption, error) {
	var opts []choiceOption
	if src, ok := q["choices_from"].(map[string]any); ok && str(src["type"]) == "source" {
		id, _ := api.ID(src["source"])
		var err error
		if opts, err = sourceOptions(c, id, answered); err != nil {
			return nil, err
		}
	} else {
		values, err := resolveChoices(c, q, sc)
		if err != nil {
			return nil, err
		}
		opts = make([]choiceOption, len(values))
		for i, v := range values {
			opts[i] = choiceOption{Value: v, Label: v}
		}
	}
	if sc.Filter == nil || q["choices_from"] == nil {
		return opts, nil
	}
	kept, err := sc.Filter(str(q["variable"]), optionValues(opts), answered)
	if err != nil {
		return nil, err
	}
	out := []choiceOption{}
	for _, o := range opts {
		if contains(kept, o.Value) {
			out = append(out, o)
		}
	}
	return out, nil
}

// choices lists a question's choices, narrowed by the context's filter.
func (sc SurveyContext) choices(c *Ctx, q map[string]any, answered map[string]any) ([]string, error) {
	opts, err := sc.options(c, q, answered)
	return optionValues(opts), err
}

func optionValues(opts []choiceOption) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Value
	}
	return out
}

// choicesReply is the reply of the survey_choices endpoints: the values,
// and their labels when a list has any.
func choicesReply(variable string, opts []choiceOption) Obj {
	labels := map[string]string{}
	for _, o := range opts {
		if o.Label != o.Value {
			labels[o.Value] = o.Label
		}
	}
	out := Obj{"variable": variable, "choices": optionValues(opts)}
	if len(labels) > 0 {
		out["labels"] = labels
	}
	return out
}

// earlierAnswers keeps the answers to the questions before variable's: a
// dynamic list depends only on those, as show_if does. Passwords are left
// out, since a choice source can send answers to an API.
func earlierAnswers(spec map[string]any, variable string, given map[string]any) map[string]any {
	out := map[string]any{}
	for _, q := range surveyQuestions(spec) {
		v := str(q["variable"])
		if v == variable {
			break
		}
		if a, ok := given[v]; ok && str(q["type"]) != "password" {
			out[v] = a
		}
	}
	return out
}

// answersParam reads the answers query parameter (a JSON object).
func answersParam(r *http.Request) (map[string]any, error) {
	given := map[string]any{}
	if s := r.URL.Query().Get("answers"); s != "" {
		if err := json.Unmarshal([]byte(s), &given); err != nil || given == nil {
			return nil, api.Detail(400, "answers must be a JSON object.")
		}
	}
	return given, nil
}

var choiceSourceTypes = map[string]bool{"hosts": true, "groups": true, "git_refs": true, "resource": true, "source": true}

// maxSurveyChoices bounds a dynamic choice list.
const maxSurveyChoices = 500

// validateChoicesFrom checks a question's choices_from.
func validateChoicesFrom(src map[string]any) error {
	typ := str(src["type"])
	if !choiceSourceTypes[typ] {
		return fmt.Errorf("'choices_from' type %q is not one of hosts, groups, git_refs, resource, source", typ)
	}
	for _, k := range []string{"inventory", "project", "source"} {
		if v, ok := src[k]; ok && v != nil {
			if _, ok := api.ID(v); !ok {
				return fmt.Errorf("'choices_from' %s must be an id", k)
			}
		}
	}
	switch typ {
	case "source":
		if _, ok := api.ID(src["source"]); !ok {
			return errors.New("'choices_from' of type source needs the source's id")
		}
	case "git_refs":
		if r := str(src["refs"]); r != "" && r != "tags" && r != "branches" && r != "all" {
			return fmt.Errorf("'choices_from' refs must be tags, branches or all")
		}
	case "resource":
		res := api.Lookup(str(src["resource"]))
		if res == nil {
			return fmt.Errorf("'choices_from' resource %q does not exist", str(src["resource"]))
		}
		if _, err := choiceField(res, str(src["field"])); err != nil {
			return err
		}
		filter, ok := src["filter"].(map[string]any)
		if !ok && src["filter"] != nil {
			return errors.New("'choices_from' filter must be an object of API filters")
		}
		arg := func(any) string { return "$1" }
		for k, v := range filter {
			if _, err := api.FilterSQL(res, arg, k, fmt.Sprint(v)); err != nil {
				return fmt.Errorf("'choices_from' filter %q: %v", k, err)
			}
		}
	}
	return nil
}

// choiceField is the field whose values make the choices (default name).
func choiceField(res *Resource, name string) (*Field, error) {
	if name == "" {
		name = "name"
	}
	for _, f := range res.Fields {
		if f.Name == name {
			if f.Type == api.TSecret || f.WriteOnly || f.Hidden || f.Virtual {
				break
			}
			return f, nil
		}
	}
	return nil, fmt.Errorf("'choices_from' field %q cannot be listed for %s", name, res.Name)
}

// resolveChoices lists the choices of a question for the user of c: its
// fixed choices, or those of its choices_from.
func resolveChoices(c *Ctx, q map[string]any, sc SurveyContext) ([]string, error) {
	src, ok := q["choices_from"].(map[string]any)
	if !ok {
		return surveyChoices(q["choices"]), nil
	}
	if sc.System {
		sys := *c
		sys.User = rbac.System()
		c = &sys
	}
	switch typ := str(src["type"]); typ {
	case "hosts", "groups":
		inv := sc.Inventory
		if id, ok := api.ID(src["inventory"]); ok {
			inv = id
		}
		if inv == 0 {
			return nil, fmt.Errorf("there is no inventory to list %s from", typ)
		}
		return listChoices(c, api.Lookup(typ), url.Values{"inventory": {fmt.Sprint(inv)}}, "name")
	case "resource":
		res := api.Lookup(str(src["resource"]))
		if res == nil {
			return nil, fmt.Errorf("resource %q does not exist", str(src["resource"]))
		}
		params := url.Values{}
		if filter, ok := src["filter"].(map[string]any); ok {
			for k, v := range filter {
				params.Set(k, fmt.Sprint(v))
			}
		}
		f, err := choiceField(res, str(src["field"]))
		if err != nil {
			return nil, err
		}
		return listChoices(c, res, params, f.Name)
	case "git_refs":
		project, ok := api.ID(src["project"])
		if !ok {
			project, ok = api.ID(sc.Template["project"])
		}
		if !ok {
			return nil, errors.New("there is no project to list git refs from")
		}
		if visible, err := api.CanView(c, api.Lookup("projects"), project); err != nil || !visible {
			return nil, errors.New("the project is not visible to you")
		}
		return projectRefs(c, c.App.Cfg.ProjectsRoot, project, str(src["refs"]))
	}
	return nil, fmt.Errorf("unknown choices_from type %q", str(src["type"]))
}

// listChoices lists distinct values of field for the objects of res the user can see.
func listChoices(c *Ctx, res *Resource, params url.Values, field string) ([]string, error) {
	if params.Get("order_by") == "" {
		params.Set("order_by", field)
	}
	objs, _, err := api.List(c, res, api.ListParams{Params: params, Page: 1, PageSize: maxSurveyChoices, NoCount: true})
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, o := range objs {
		if o[field] == nil {
			continue
		}
		s := fmt.Sprint(o[field])
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// projectRefs lists the tags (newest first), branches or both of a project's
// fetch clone on this node, as of its last sync.
func projectRefs(ctx context.Context, root string, project int64, kind string) ([]string, error) {
	repo := ProjectRepoDir(root, project)
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return nil, errors.New("the project has not been synced yet")
	}
	prefixes := map[string][]string{
		"tags":     {"refs/tags/"},
		"branches": {"refs/remotes/origin/"},
		"all":      {"refs/tags/", "refs/remotes/origin/"},
	}[kind]
	if prefixes == nil {
		prefixes = []string{"refs/tags/"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Newest first; refs created in the same second by version, highest first (the last --sort is the primary key).
	args := append([]string{"for-each-ref", "--sort=-version:refname", "--sort=-creatordate", "--format=%(refname)", "--count=" + fmt.Sprint(maxSurveyChoices)}, prefixes...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing git refs failed: %v", err)
	}
	refs := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, p := range prefixes {
			if name, ok := strings.CutPrefix(line, p); ok && name != "HEAD" {
				refs = append(refs, name)
			}
		}
	}
	return refs, nil
}

// handleSurveyChoices serves /<templates>/<id>/survey_choices/?variable=x
// with the choices the requesting user may pick for question x. An
// inventory parameter lists hosts and groups of the inventory chosen at
// launch, when the template prompts for one; answers (JSON) are the earlier
// answers a choice source may use.
func handleSurveyChoices(c *Ctx, w http.ResponseWriter, r *http.Request, tpl Obj) {
	id, _ := api.ID(tpl["id"])
	variable := r.URL.Query().Get("variable")
	given, err := answersParam(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	spec := templateSurvey(c, id)
	for _, q := range surveyQuestions(spec) {
		if str(q["variable"]) != variable {
			continue
		}
		sc := SurveyContext{Template: tpl}
		sc.Inventory, _ = api.ID(tpl["inventory"])
		if inv, ok := api.ID(r.URL.Query().Get("inventory")); ok && boolv(tpl["ask_inventory_on_launch"]) {
			sc.Inventory = inv
		}
		opts, err := sc.options(c, q, earlierAnswers(spec, variable, given))
		if err != nil {
			api.WriteJSON(w, 200, Obj{"variable": variable, "choices": []string{}, "error": err.Error()})
			return
		}
		api.WriteJSON(w, 200, choicesReply(variable, opts))
		return
	}
	api.WriteError(w, api.Detail(404, fmt.Sprintf("The survey has no question for %q.", variable)))
}

// surveyQuestions returns the questions of a survey spec.
func surveyQuestions(spec map[string]any) []map[string]any {
	var out []map[string]any
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		if qm, ok := q.(map[string]any); ok {
			out = append(out, qm)
		}
	}
	return out
}

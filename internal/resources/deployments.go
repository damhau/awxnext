package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/outputs"
	"github.com/damhau/awxnext/internal/rbac"
)

// Deployments (awxnext, roadmap M15-05): what a request of an item that
// creates one provisioned. Its owners are the requester and the request's
// team, and its outputs are the declared outputs of its runs, secret ones
// kept encrypted. Its day-2 actions and its deletion are requests on it,
// held by the item's rules as any request is; their kind is the action's id,
// or delete.

const (
	deploymentActive       = "active"
	deploymentDeleting     = "deleting"
	deploymentDeleteFailed = "delete_failed"
	deploymentDeleted      = "deleted"

	requestDelete = "delete"

	maxConnectionEntries = 30
	maxItemActions       = 20
	maxDeploymentName    = 512
)

var (
	actionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// reservedKinds are request kinds that an action can't take as its id.
	reservedKinds      = map[string]bool{requestNew: true, requestDelete: true, "extend": true}
	connectionKeys     = map[string]bool{"label": true, "value": true, "copy": true}
	itemActionKeys     = map[string]bool{"id": true, "name": true, "description": true, "template": true}
	actionRequestKeys  = map[string]bool{"action": true, "answers": true, "reason": true}
	unfinishedRequests = []string{requestAwaiting, requestPending, requestRunning}
)

type connectionEntry struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Copy  bool   `json:"copy"`
}

type itemAction struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Template    int64  `json:"template"`
}

func registerDeployments() {
	api.Register(&Resource{
		Name:         "deployments",
		Type:         "deployment",
		Table:        "deployments",
		Verbose:      "Deployment",
		Ordering:     "-id",
		NoCreate:     true,
		SearchFields: []string{"name", "description", "item_name"},
		Fields: []*Field{
			{Name: "catalog_item", Type: api.TFK, Ref: "catalog_items", ReadOnly: true, Nullable: true},
			{Name: "item_name", Type: api.TString, ReadOnly: true, Help: "The item's name when it was requested."},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true},
			{Name: "request", Type: api.TFK, Ref: "catalog_requests", ReadOnly: true, Nullable: true, Help: "The request that created it."},
			{Name: "owner", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true,
				Help: "The user who owns it: its requester, or whom it was handed over to. Empty: its team owns it."},
			{Name: "team", Type: api.TFK, Ref: "teams", ReadOnly: true, Nullable: true, Help: "The team that owns it with its owner."},
			{Name: "status", Type: api.TChoice, ReadOnly: true, Default: deploymentActive, Choices: labelled(deploymentActive, "Active",
				deploymentDeleting, "Being deleted", deploymentDeleteFailed, "Couldn't be deleted", deploymentDeleted, "Deleted")},
			{Name: "explanation", Type: api.TText, ReadOnly: true, Help: "Why it couldn't be deleted."},
			{Name: "outputs", Type: api.TJSON, ReadOnly: true, Redact: redactOutputs,
				Help: "The declared outputs of its runs, by name; secret ones show as $encrypted$."},
			{Name: "left_behind", Type: api.TJSON, ReadOnly: true, Nullable: true,
				Help: "What its teardown said it left, from its left_behind artifact."},
			{Name: "expires_at", Type: api.TDateTime, Nullable: true,
				Help: "When it expires and its teardown runs. Empty: it is kept for good. Its owners extend it; only the item's admins set it."},
			{Name: "extensions", Type: api.TInt, ReadOnly: true, Default: 0, Help: "How many times it was extended."},
			{Name: "deleted_at", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "connection", Type: api.TJSON, ReadOnly: true, Virtual: true,
				Help: "How to connect, as the item lays it out: each entry's label, value, and whether it is secret (shown through reveal) or copied."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			a := rbac.ArgFunc(arg)
			admin := c.User.Predicate(a, "catalogitem", "change_catalogitem", rbac.Scope{ID: "t.catalog_item_id", Org: "t.organization_id"})
			audit := c.User.Predicate(a, "organization", "audit_organization", rbac.Scope{ID: "t.organization_id", Org: "t.organization_id"})
			return fmt.Sprintf("%s OR %s OR %s", ownedPredicate(c, arg, "t"), admin, audit)
		},
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			if perm != "change" && perm != "delete" {
				return false, nil
			}
			if ownsDeployment(c, obj) {
				return true, nil
			}
			return deploymentAdmin(c, obj)
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			ok, err := api.HasPerm(c, api.Lookup("deployments"), obj, "delete")
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return deploymentReady(c, obj, deploymentActive, deploymentDeleteFailed)
		},
		BeforeSave: checkExpiryChange,
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			if sameTime(old["expires_at"], obj["expires_at"]) {
				return nil
			}
			_, err := c.DB().Exec(c, "UPDATE deployments SET reminded_hours = NULL WHERE id = $1", obj["id"])
			return err
		},
		SoftDelete:   func(c *Ctx, obj Obj) error { return requestDeletion(c, obj) },
		FinishDelete: func(app *api.App, obj Obj) { announceHeld(app, obj) },
		Summary: func(c *Ctx, objs []Obj) error {
			layouts := map[int64][]connectionEntry{}
			for _, o := range objs {
				o["connection"] = deploymentConnection(c, o, itemLayout(c, o["catalog_item"], layouts))
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["outputs"] = redactOutputs(o["outputs"])
			if rel, ok := o["related"].(Obj); ok {
				base := api.Lookup("deployments").URL(o["id"])
				rel["actions"] = base + "actions/"
				rel["extend"] = base + "extend/"
				rel["hand_over"] = base + "hand_over/"
				rel["reveal"] = base + "reveal/"
			}
		},
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			owns := ownsDeployment(c, obj)
			admin, _ := deploymentAdmin(c, obj)
			status := str(obj["status"])
			return map[string]bool{
				"edit":      owns || admin,
				"delete":    (owns || admin) && (status == deploymentActive || status == deploymentDeleteFailed),
				"run":       owns && status == deploymentActive,
				"extend":    owns && status == deploymentActive && obj["expires_at"] != nil,
				"hand_over": (owns || admin) && status == deploymentActive,
				"reveal":    owns && status != deploymentDeleted,
			}
		},
		SubLists: []*SubList{readOnlyChildSub("requests", "catalog_requests", "deployment_id")},
		Actions: []*api.Action{
			{Name: "actions", Methods: []string{"GET", "POST"}, Handler: handleDeploymentActions},
			{Name: "extend", Methods: []string{"GET", "POST"}, Handler: handleExtend},
			{Name: "hand_over", Methods: []string{"POST"}, Handler: handleHandOver},
			{Name: "reveal", Methods: []string{"POST"}, Handler: handleReveal},
		},
	})
}

// ownedPredicate selects the deployments, under alias, that the user owns.
func ownedPredicate(c *Ctx, arg ArgFunc, alias string) string {
	teams := c.User.Teams
	if teams == nil {
		teams = []int64{}
	}
	return fmt.Sprintf("(%[1]s.owner_id = %[2]s OR %[1]s.team_id = ANY(%[3]s))", alias, arg(c.User.UserID), arg(teams))
}

// ownsDeployment: its owner and the members of its team own it.
func ownsDeployment(c *Ctx, d Obj) bool {
	if c.User.Anonymous() {
		return false
	}
	if owner, ok := api.ID(d["owner"]); ok && owner == c.User.UserID {
		return true
	}
	team, ok := api.ID(d["team"])
	return ok && slices.Contains(c.User.Teams, team)
}

// deploymentAdmin: the item's admins, or the organization's once the item
// is gone.
func deploymentAdmin(c *Ctx, d Obj) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	if item, ok := api.ID(d["catalog_item"]); ok {
		ok, err := api.HasPermID(c, "catalog_items", item, "change")
		if !errors.Is(err, api.ErrNotFound) {
			return ok, err
		}
	}
	org, _ := api.ID(d["organization"])
	ok, err := api.HasPermID(c, "organizations", org, "change")
	if errors.Is(err, api.ErrNotFound) {
		return false, nil
	}
	return ok, err
}

// deploymentReady refuses a request on a deployment in none of the
// statuses, or with a request on it that hasn't finished.
func deploymentReady(c *Ctx, d Obj, statuses ...string) error {
	status := str(d["status"])
	if !slices.Contains(statuses, status) {
		switch status {
		case deploymentDeleting:
			return api.BadRequest("The deployment is being deleted.")
		case deploymentDeleted:
			return api.BadRequest("The deployment is deleted.")
		case deploymentDeleteFailed:
			return api.BadRequest("The deployment couldn't be deleted: delete it again, or ask its item's owners.")
		}
		return api.BadRequest("The deployment isn't active.")
	}
	var open int64
	err := c.DB().QueryRow(c, "SELECT id FROM catalog_requests WHERE deployment_id = $1 AND status = ANY($2) ORDER BY id LIMIT 1",
		d["id"], unfinishedRequests).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return api.BadRequest(fmt.Sprintf("Request %d on this deployment hasn't finished: wait for it.", open))
}

// systemItem is a deployment's item, read as the system: its owners need
// no role on it.
func systemItem(c *Ctx, d Obj) (Obj, error) {
	id, ok := api.ID(d["catalog_item"])
	if !ok {
		return nil, api.BadRequest("The deployment's catalog item was deleted.")
	}
	sys := *c
	sys.User = rbac.System()
	item, err := api.Get(&sys, api.Lookup("catalog_items"), id)
	if errors.Is(err, api.ErrNotFound) {
		return nil, api.BadRequest("The deployment's catalog item was deleted.")
	}
	return item, err
}

// loadTemplate reads a job or workflow template as the system.
func loadTemplate(c *Ctx, id int64) (*Resource, Obj, error) {
	res := api.Lookup(ujtType(c, id) + "s")
	if res == nil || (res.Name != "job_templates" && res.Name != "workflow_job_templates") {
		return nil, nil, api.ErrNotFound
	}
	sys := *c
	sys.User = rbac.System()
	tpl, err := api.Get(&sys, res, id)
	return res, tpl, err
}

func itemActions(v any) []itemAction {
	var out []itemAction
	b, _ := json.Marshal(v)
	json.Unmarshal(b, &out)
	return out
}

func findAction(item Obj, id string) (itemAction, bool) {
	for _, a := range itemActions(item["actions"]) {
		if a.ID == id {
			return a, true
		}
	}
	return itemAction{}, false
}

// itemRuleSpec is what an item's rules read by variable: the questions of
// its template's survey and of its actions' templates' surveys, the first
// of a variable winning.
func itemRuleSpec(c *Ctx, ujt int64, actions []itemAction) map[string]any {
	seen := map[string]bool{}
	qs := []any{}
	add := func(spec map[string]any) {
		for _, q := range surveyQuestions(spec) {
			if v := str(q["variable"]); !seen[v] {
				seen[v] = true
				qs = append(qs, q)
			}
		}
	}
	add(enabledSurvey(c, ujt))
	for _, a := range actions {
		add(enabledSurvey(c, a.Template))
	}
	return map[string]any{"spec": qs}
}

// checkDeploymentFields checks what an item says of its deployments: their
// name, how to connect, their actions and their teardown.
func checkDeploymentFields(c *Ctx, old, data Obj, pick func(string) any, org int64) error {
	conn, err := checkConnection(pick("connection"))
	if err != nil {
		return err
	}
	var prev []itemAction
	if old != nil {
		prev = itemActions(old["actions"])
	}
	acts, err := checkItemActions(c, pick("actions"), org, prev)
	if err != nil {
		return err
	}
	name, isStr := pick("deployment_name").(string)
	if pick("deployment_name") != nil && !isStr {
		return api.FieldError("deployment_name", "Not a valid string.")
	}
	name = strings.TrimSpace(name)
	if name != "" {
		if err := checkExpr(name); err != nil {
			return api.FieldError("deployment_name", err.Error())
		}
	}
	teardown, hasTeardown := api.ID(pick("teardown_template"))
	if pick("kind") != itemDeployment {
		for field, set := range map[string]bool{"connection": len(conn) > 0, "actions": len(acts) > 0, "teardown_template": hasTeardown, "deployment_name": name != ""} {
			if set {
				return api.FieldError(field, "Only an item that creates a deployment has this.")
			}
		}
	}
	if hasTeardown {
		if err := checkItemTemplate(c, "teardown_template", teardown, org); err != nil {
			return err
		}
		if old == nil || !sameValue(old["teardown_template"], teardown) {
			if err := requireTemplateAdmin(c, "teardown_template", teardown); err != nil {
				return err
			}
		}
	}
	data["connection"], data["actions"], data["deployment_name"] = conn, acts, name
	return nil
}

func checkConnection(v any) ([]any, error) {
	bad := func(msg string) error { return api.FieldError("connection", msg) }
	list, ok := v.([]any)
	if v == nil {
		list, ok = []any{}, true
	}
	if !ok {
		return nil, bad("Expected a list of entries, each a label and a value.")
	}
	if len(list) > maxConnectionEntries {
		return nil, bad(fmt.Sprintf("At most %d entries.", maxConnectionEntries))
	}
	labels := map[string]bool{}
	out := []any{}
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, bad(fmt.Sprintf("Entry %d: expected an object.", i))
		}
		for k := range m {
			if !connectionKeys[k] {
				return nil, bad(fmt.Sprintf("Entry %d: %s is not one of label, value, copy.", i, k))
			}
		}
		e := connectionEntry{Label: strings.TrimSpace(str(m["label"])), Value: strings.TrimSpace(str(m["value"])), Copy: true}
		if e.Label == "" || len(e.Label) > 128 {
			return nil, bad(fmt.Sprintf("Entry %d: a label of 1 to 128 characters is required.", i))
		}
		if labels[e.Label] {
			return nil, bad(fmt.Sprintf("Two entries are labelled %q.", e.Label))
		}
		labels[e.Label] = true
		if e.Value == "" {
			return nil, bad(fmt.Sprintf("%s: value is required: an expression over the outputs, such as outputs.host.", e.Label))
		}
		if err := checkExpr(e.Value); err != nil {
			return nil, bad(fmt.Sprintf("%s: value: %v", e.Label, err))
		}
		if x, given := m["copy"]; given && x != nil {
			b, isBool := x.(bool)
			if !isBool {
				return nil, bad(fmt.Sprintf("%s: copy must be true or false.", e.Label))
			}
			e.Copy = b
		}
		out = append(out, map[string]any{"label": e.Label, "value": e.Value, "copy": e.Copy})
	}
	return out, nil
}

func checkItemActions(c *Ctx, v any, org int64, prev []itemAction) ([]any, error) {
	bad := func(msg string) error { return api.FieldError("actions", msg) }
	list, ok := v.([]any)
	if v == nil {
		list, ok = []any{}, true
	}
	if !ok {
		return nil, bad("Expected a list of actions, each an id, a name and a template.")
	}
	if len(list) > maxItemActions {
		return nil, bad(fmt.Sprintf("An item has at most %d actions.", maxItemActions))
	}
	published := map[int64]bool{}
	for _, a := range prev {
		published[a.Template] = true
	}
	ids := map[string]bool{}
	out := []any{}
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, bad(fmt.Sprintf("Action %d: expected an object.", i))
		}
		for k := range m {
			if !itemActionKeys[k] {
				return nil, bad(fmt.Sprintf("Action %d: %s is not one of id, name, description, template.", i, k))
			}
		}
		a := itemAction{ID: str(m["id"]), Name: strings.TrimSpace(str(m["name"])), Description: strings.TrimSpace(str(m["description"]))}
		if !actionIDPattern.MatchString(a.ID) || reservedKinds[a.ID] {
			return nil, bad(fmt.Sprintf("Action %d: id must be lowercase letters, digits and underscores, starting with a letter, and not new, extend or delete.", i))
		}
		if ids[a.ID] {
			return nil, bad(fmt.Sprintf("Two actions have the id %q.", a.ID))
		}
		ids[a.ID] = true
		if a.Name == "" || len(a.Name) > 128 {
			return nil, bad(fmt.Sprintf("%s: a name of 1 to 128 characters is required.", a.ID))
		}
		if len(a.Description) > 512 {
			return nil, bad(fmt.Sprintf("%s: a description of at most 512 characters.", a.ID))
		}
		tpl, ok := api.ID(m["template"])
		if !ok {
			return nil, bad(fmt.Sprintf("%s: template is required: the job or workflow template the action runs.", a.ID))
		}
		if err := checkItemTemplate(c, "actions", tpl, org); err != nil {
			return nil, err
		}
		if !published[tpl] {
			if err := requireTemplateAdmin(c, "actions", tpl); err != nil {
				return nil, err
			}
		}
		a.Template = tpl
		out = append(out, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "template": a.Template})
	}
	return out, nil
}

// checkItemTemplate: what an item runs is a job or workflow template of
// its organization.
func checkItemTemplate(c *Ctx, field string, id, org int64) error {
	var typ string
	var tplOrg *int64
	if err := c.DB().QueryRow(c, "SELECT type, organization_id FROM unified_job_templates WHERE id = $1", id).Scan(&typ, &tplOrg); err != nil {
		return api.FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	if typ != "job_template" && typ != "workflow_job_template" {
		return api.FieldError(field, "Expected a job template or a workflow template.")
	}
	if tplOrg == nil || *tplOrg != org {
		return api.FieldError(field, "The template must be of the item's organization.")
	}
	return nil
}

// requireTemplateAdmin: publishing a template runs it for requesters who
// have no role on it, as publishing the item's own does.
func requireTemplateAdmin(c *Ctx, field string, id int64) error {
	ok, err := ujtPerm(c, id, "change")
	if err != nil && !errors.Is(err, api.ErrNotFound) {
		return err
	}
	if !ok {
		return api.FieldError(field, fmt.Sprintf("You need the Admin role on template %d to publish it.", id))
	}
	return nil
}

// redactOutputs masks the secret outputs.
func redactOutputs(v any) any {
	outs, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(outs))
	for k, x := range outs {
		if s, _ := x.(string); crypto.IsEncrypted(s) {
			x = crypto.Placeholder
		}
		out[k] = x
	}
	return out
}

func secretOutputs(outs map[string]any) map[string]bool {
	out := map[string]bool{}
	for k, v := range outs {
		if s, _ := v.(string); crypto.IsEncrypted(s) || s == crypto.Placeholder {
			out[k] = true
		}
	}
	return out
}

// itemLayout is an item's connection layout, cached by item.
func itemLayout(c *Ctx, item any, cache map[int64][]connectionEntry) []connectionEntry {
	id, ok := api.ID(item)
	if !ok {
		return nil
	}
	if l, ok := cache[id]; ok {
		return l
	}
	var raw []byte
	c.DB().QueryRow(c, "SELECT connection FROM catalog_items WHERE id = $1", id).Scan(&raw)
	var l []connectionEntry
	json.Unmarshal(raw, &l)
	cache[id] = l
	return l
}

// layoutOf is the layout to show: the item's, or every output under its
// name.
func layoutOf(layout []connectionEntry, outs map[string]any) []connectionEntry {
	if len(layout) > 0 {
		return layout
	}
	names := slices.Sorted(maps.Keys(outs))
	out := make([]connectionEntry, len(names))
	for i, n := range names {
		out[i] = connectionEntry{Label: n, Value: fmt.Sprintf("outputs[%q]", n), Copy: true}
	}
	return out
}

// connectionEnv is what a connection value reads: each output by name,
// outputs, and the deployment's id and name.
func connectionEnv(d Obj, outs map[string]any) map[string]any {
	env := maps.Clone(outs)
	if env == nil {
		env = map[string]any{}
	}
	env["outputs"] = outs
	env["deployment"] = map[string]any{"id": d["id"], "name": d["name"]}
	return env
}

func deploymentConnection(c *Ctx, d Obj, layout []connectionEntry) []Obj {
	outs, _ := d["outputs"].(map[string]any)
	secrets := secretOutputs(outs)
	env := connectionEnv(d, redactOutputs(outs).(map[string]any))
	list := []Obj{}
	for _, e := range layoutOf(layout, outs) {
		entry := Obj{"label": e.Label, "value": "", "copy": e.Copy, "secret": false}
		secret, err := readsSecret(e.Value, secrets)
		switch {
		case err != nil:
			entry["error"] = err.Error()
		case secret:
			entry["secret"], entry["value"] = true, crypto.Placeholder
		default:
			if v, err := evalExpr(e.Value, env); err != nil {
				entry["error"] = err.Error()
			} else {
				entry["value"] = displayValue(v)
			}
		}
		list = append(list, entry)
	}
	return list
}

func displayValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// readsSecret says whether an expression reads a secret output: by name,
// through outputs or $env by a constant key, or through outputs or $env
// whole or by a computed key.
func readsSecret(src string, secrets map[string]bool) (bool, error) {
	tree, err := parser.Parse(src)
	if err != nil {
		return false, err
	}
	if len(secrets) == 0 {
		return false, nil
	}
	members := &memberRefs{secrets: secrets, keyed: map[ast.Node]bool{}}
	ast.Walk(&tree.Node, members)
	if members.hit {
		return true, nil
	}
	names := &nameRefs{secrets: secrets, keyed: members.keyed}
	ast.Walk(&tree.Node, names)
	return names.hit, nil
}

type memberRefs struct {
	secrets map[string]bool
	keyed   map[ast.Node]bool
	hit     bool
}

func (v *memberRefs) Visit(node *ast.Node) {
	m, ok := (*node).(*ast.MemberNode)
	if !ok {
		return
	}
	id, ok := m.Node.(*ast.IdentifierNode)
	if !ok || (id.Value != "outputs" && id.Value != "$env") {
		return
	}
	if key, ok := m.Property.(*ast.StringNode); ok {
		v.keyed[id] = true
		if v.secrets[key.Value] {
			v.hit = true
		}
	}
}

type nameRefs struct {
	secrets map[string]bool
	keyed   map[ast.Node]bool
	hit     bool
}

func (v *nameRefs) Visit(node *ast.Node) {
	id, ok := (*node).(*ast.IdentifierNode)
	if !ok {
		return
	}
	if v.secrets[id.Value] || ((id.Value == "outputs" || id.Value == "$env") && !v.keyed[id]) {
		v.hit = true
	}
}

// deploymentName is a new deployment's name: the item's expression over
// the request's answers and the outputs, or the item's name and the
// request's id.
func deploymentName(src, item string, request int64, answers, outs map[string]any) string {
	name := ""
	if src != "" {
		env := map[string]any{"answers": answers, "outputs": redactOutputs(outs), "request": map[string]any{"id": request}, "item": item}
		if v, err := evalExpr(src, env); err == nil {
			name = strings.TrimSpace(displayValue(v))
		} else {
			slog.Warn("a deployment's name", "catalog_request", request, "err", err)
		}
	}
	if name == "" || name == crypto.Placeholder {
		name = fmt.Sprintf("%s %d", item, request)
	}
	if len(name) > maxDeploymentName {
		name = name[:maxDeploymentName]
	}
	return name
}

// jobOutputs are the declared outputs a job published, or, for a workflow
// job, those of its successful node jobs, the later winning.
func jobOutputs(c *Ctx, job int64, workflow bool) map[string]any {
	q := `SELECT j.artifacts, coalesce(t.outputs, '[]'::jsonb) FROM unified_jobs j
		LEFT JOIN unified_job_templates t ON t.id = j.unified_job_template_id WHERE j.id = $1`
	if workflow {
		q = `SELECT j.artifacts, coalesce(t.outputs, '[]'::jsonb) FROM workflow_node_jobs n JOIN unified_jobs j ON j.id = n.job_id
			LEFT JOIN unified_job_templates t ON t.id = j.unified_job_template_id
			WHERE n.workflow_job_id = $1 AND j.status = 'successful' ORDER BY j.finished, j.id`
	}
	out := map[string]any{}
	rows, err := c.DB().Query(c, q, job)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var art, decl []byte
		if rows.Scan(&art, &decl) != nil {
			continue
		}
		var published map[string]any
		json.Unmarshal(art, &published)
		for _, d := range outputs.Load(decl) {
			if v, ok := published[d.Name]; ok {
				out[d.Name] = v
			}
		}
	}
	return out
}

// leftBehind is what a teardown job, or the last node job of a teardown
// workflow that said, published as left_behind.
func leftBehind(c *Ctx, job int64, workflow bool) any {
	q := "SELECT artifacts FROM unified_jobs WHERE id = $1"
	if workflow {
		q = `SELECT j.artifacts FROM workflow_node_jobs n JOIN unified_jobs j ON j.id = n.job_id
			WHERE n.workflow_job_id = $1 AND j.artifacts ? 'left_behind' ORDER BY j.finished DESC NULLS LAST, j.id DESC LIMIT 1`
	}
	var raw []byte
	if c.DB().QueryRow(c, q, job).Scan(&raw) != nil {
		return nil
	}
	var art map[string]any
	json.Unmarshal(raw, &art)
	return art["left_behind"]
}

// outputVars are a deployment's outputs as a launch's variables: secret
// ones stay encrypted and are passed as passwords. Ansible's variables are
// left out: an output must not change how a job connects.
func outputVars(outs map[string]any) (map[string]any, []string) {
	vars := map[string]any{}
	var secret []string
	for k, v := range outs {
		if isAnsibleVar(k) {
			continue
		}
		vars[k] = v
		if s, _ := v.(string); crypto.IsEncrypted(s) {
			secret = append(secret, k)
		}
	}
	sort.Strings(secret)
	return vars, secret
}

// withAnswers lays survey answers over a deployment's outputs.
func withAnswers(outs map[string]any, sv map[string]any, pw []string) (map[string]any, []string) {
	vars, secret := outputVars(outs)
	maps.Copy(vars, sv)
	secret = slices.DeleteFunc(secret, func(k string) bool { _, answered := sv[k]; return answered })
	return vars, append(secret, pw...)
}

// pendingRequest is a request about to be made: what it is for, what it
// launches, and what the item's rules read of it.
type pendingRequest struct {
	ci            *catalogItem
	kind          string
	deployment    any
	statuses      []string
	team          any
	reason        string
	lifetime      any
	shown, labels map[string]any
	launch        requestLaunch
	env           *ruleEnv
	what          string
	// ungated skips the item's rules and freezes: an expiry.
	ungated bool
}

// submit records the request and holds it for its gates, or starts it.
func (p *pendingRequest) submit(c *Ctx, id int64, created time.Time) (Obj, []int64, error) {
	var gates []*gate
	var err error
	if !p.ungated {
		if gates, err = requestGates(c, p.ci, p.env, c.User.UserID, created); err != nil {
			return nil, nil, err
		}
	}
	answersJSON, _ := json.Marshal(p.shown)
	labelsJSON, _ := json.Marshal(p.labels)
	status, launch := requestPending, []byte(nil)
	if len(gates) > 0 {
		status = requestAwaiting
		if launch, err = json.Marshal(p.launch); err != nil {
			return nil, nil, err
		}
	}
	var req Obj
	var approvals []int64
	err = c.WithTx(func(c *Ctx) error {
		if dep, ok := api.ID(p.deployment); ok {
			d, err := lockDeployment(c, dep)
			if err != nil {
				return err
			}
			if err := deploymentReady(c, d, p.statuses...); err != nil {
				return err
			}
		}
		if _, err := c.DB().Exec(c, `INSERT INTO catalog_requests (id, created, modified, created_by_id, modified_by_id, catalog_item_id, item_name, organization_id, team_id,
			answers, answer_labels, reason, lifetime_days, kind, status, launch, deployment_id) VALUES ($1, $2, $2, $3, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			id, created, nullID(c.User.UserID), p.ci.item["id"], p.ci.item["name"], p.ci.item["organization"], p.team, string(answersJSON), string(labelsJSON), p.reason, p.lifetime,
			p.kind, status, launch, p.deployment); err != nil {
			return err
		}
		var err error
		if len(gates) > 0 {
			approvals, err = holdRequest(c, p.ci, id, gates, p.team, p.reason, p.what)
		} else {
			_, err = startRequest(c, id, p.kind, p.deployment, p.ci, p.launch)
		}
		if err != nil {
			return err
		}
		res := api.Lookup("catalog_requests")
		if req, err = api.Get(c, res, id); err != nil {
			return err
		}
		return api.Activity(c, "create", res, req, api.ActivityFields(res, req), nil, nil, "")
	})
	return req, approvals, err
}

func lockDeployment(c *Ctx, id int64) (Obj, error) {
	if _, err := c.DB().Exec(c, "SELECT 1 FROM deployments WHERE id = $1 FOR UPDATE", id); err != nil {
		return nil, err
	}
	sys := *c
	sys.User = rbac.System()
	return api.Get(&sys, api.Lookup("deployments"), id)
}

// startRequest launches what a request runs, as c's user, and returns its
// job: none for the deletion of a deployment whose item has no teardown,
// which is deleted at once.
func startRequest(c *Ctx, id int64, kind string, deployment any, ci *catalogItem, l requestLaunch) (int64, error) {
	if kind == requestNew {
		return launchRequest(c, ci, id, l.Vars, l.Passwords, l.ActivityVars)
	}
	dep, _ := api.ID(deployment)
	if kind == requestExtend {
		return 0, extendDeployment(c, id, dep, ci.item)
	}
	if kind == requestDelete && l.Template == 0 {
		if _, err := c.DB().Exec(c, "UPDATE catalog_requests SET status = $2, modified = now() WHERE id = $1", id, requestSuccessful); err != nil {
			return 0, err
		}
		return 0, setDeploymentStatus(c, dep, deploymentDeleted, "", nil)
	}
	res, tpl, err := loadTemplate(c, l.Template)
	if err != nil {
		return 0, api.BadRequest("The template this request runs no longer exists.")
	}
	job, err := launchTemplate(c, res, tpl, id, l.Vars, l.Passwords, l.ActivityVars)
	if err != nil {
		return 0, err
	}
	if kind == requestDelete {
		return job, setDeploymentStatus(c, dep, deploymentDeleting, "", nil)
	}
	return job, nil
}

// setDeploymentStatus records a deployment's new status, and what was left
// behind when it is deleted or couldn't be.
func setDeploymentStatus(c *Ctx, id int64, status, explanation string, left any) error {
	sys := *c
	sys.User = rbac.System()
	res := api.Lookup("deployments")
	old, err := api.Get(&sys, res, id)
	if err != nil {
		return err
	}
	var leftJSON any
	if left != nil {
		b, _ := json.Marshal(left)
		leftJSON = string(b)
	}
	if _, err := c.DB().Exec(c, `UPDATE deployments SET status = $2, explanation = $3, modified = now(),
		left_behind = CASE WHEN $2 = 'deleting' THEN left_behind ELSE $4::jsonb END,
		deleted_at = CASE WHEN $2 = 'deleted' THEN now() END WHERE id = $1`, id, status, explanation, leftJSON); err != nil {
		return err
	}
	if old["status"] == status {
		return nil
	}
	return api.Activity(c, "update", res, old, Obj{"status": []any{old["status"], status}}, nil, nil, "")
}

// launchTemplate runs a request's template with its variables, as c's
// user.
func launchTemplate(c *Ctx, res *Resource, tpl Obj, id int64, sv map[string]any, pw []string, keep map[string]bool) (int64, error) {
	lc := *c
	lc.Extra = maps.Clone(c.Extra)
	if lc.Extra == nil {
		lc.Extra = map[string]any{}
	}
	lc.Extra["activity_vars"] = keep
	ov := &LaunchOverrides{Survey: &SurveyAnswers{Vars: sv, Passwords: pw}, ExtraJob: map[string]any{"catalog_request_id": id}}
	var jobID int64
	var err error
	if res.Name == "workflow_job_templates" {
		jobID, _, err = LaunchWorkflowJobTemplate(&lc, tpl, Obj{}, "manual", ov)
	} else {
		jobID, _, err = LaunchJobTemplate(&lc, tpl, Obj{}, "manual", ov)
	}
	if err != nil {
		return 0, err
	}
	_, err = c.DB().Exec(c, "UPDATE catalog_requests SET unified_job_id = $2 WHERE id = $1", id, jobID)
	return jobID, err
}

// deploymentRequestValid says why an approved request on a deployment
// can't run any more, and sets what a deletion runs: the item's teardown
// as it is now.
func deploymentRequestValid(c *Ctx, deployment *int64, kind string, item Obj, l *requestLaunch) string {
	if deployment == nil {
		return "the deployment was deleted."
	}
	sys := *c
	sys.User = rbac.System()
	d, err := api.Get(&sys, api.Lookup("deployments"), *deployment)
	if err != nil {
		return "the deployment was deleted."
	}
	if !ownsDeployment(c, d) {
		if ok, _ := deploymentAdmin(c, d); !ok || kind != requestDelete {
			return "its requester no longer owns the deployment."
		}
	}
	statuses := []string{deploymentActive}
	switch kind {
	case requestDelete:
		statuses = append(statuses, deploymentDeleteFailed)
		l.Template, _ = api.ID(item["teardown_template"])
	case requestExtend:
	default:
		if a, ok := findAction(item, kind); !ok || a.Template != l.Template {
			return "the item's action changed since: request it again."
		}
	}
	if !slices.Contains(statuses, str(d["status"])) {
		return fmt.Sprintf("the deployment is %s.", strings.ReplaceAll(str(d["status"]), "_", " "))
	}
	return ""
}

// requestDeletion requests a deployment's deletion: its teardown runs with
// its outputs, once the item's rules let it.
func requestDeletion(c *Ctx, d Obj) error {
	if d["catalog_item"] == nil {
		id, _ := api.ID(d["id"])
		if err := deploymentReady(c, d, deploymentActive, deploymentDeleteFailed); err != nil {
			return err
		}
		return setDeploymentStatus(c, id, deploymentDeleted, "", nil)
	}
	item, err := systemItem(c, d)
	if err != nil {
		return err
	}
	id, created, err := nextRequest(c)
	if err != nil {
		return err
	}
	p := deletionRequest(c, d, item, id, created)
	_, approvals, err := p.submit(c, id, created)
	if err != nil {
		return err
	}
	d["_held"] = &heldRequest{item: item, request: id, approvals: approvals}
	return nil
}

// deletionRequest is the request that runs a deployment's teardown with its
// outputs.
func deletionRequest(c *Ctx, d, item Obj, id int64, created time.Time) *pendingRequest {
	outs, _ := d["outputs"].(map[string]any)
	vars, pw := outputVars(outs)
	teardown, _ := api.ID(item["teardown_template"])
	return &pendingRequest{
		ci: &catalogItem{item: item}, kind: requestDelete, deployment: d["id"], statuses: []string{deploymentActive, deploymentDeleteFailed},
		team: d["team"], shown: map[string]any{}, labels: map[string]any{},
		launch: requestLaunch{Vars: vars, Passwords: pw, ActivityVars: map[string]bool{}, Template: teardown},
		env: &ruleEnv{answers: map[string]any{}, picked: map[string]any{}, team: teamEnv(c, d["team"]), user: userEnv(c, c.User.UserID),
			request: requestEnv(id, created, nil), kind: requestDelete},
		what: "delete " + str(d["name"]),
	}
}

type heldRequest struct {
	item      Obj
	request   int64
	approvals []int64
}

// announceHeld mails the approvers of a deletion its rules hold, once
// the deletion is committed.
func announceHeld(app *api.App, d Obj) {
	h, _ := d["_held"].(*heldRequest)
	if h == nil || len(h.approvals) == 0 {
		return
	}
	c := &Ctx{Context: context.Background(), App: app, User: rbac.System(), Extra: map[string]any{}}
	announceApprovals(c, &catalogItem{item: h.item}, h.request, h.approvals)
}

func handleDeploymentActions(c *Ctx, w http.ResponseWriter, r *http.Request, d Obj) {
	admin, err := deploymentAdmin(c, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	owns := ownsDeployment(c, d)
	if !owns && (!admin || r.Method != "GET") {
		api.WriteError(w, api.Detail(403, "Only the deployment's owners run its actions."))
		return
	}
	item, err := systemItem(c, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if r.Method == "GET" {
		list := []Obj{}
		for _, a := range itemActions(item["actions"]) {
			list = append(list, Obj{"id": a.ID, "name": a.Name, "description": a.Description, "questions": actionQuestions(c, a.Template)})
		}
		api.WriteJSON(w, 200, Obj{"actions": list})
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	req, err := RequestDeploymentAction(c, d, item, normalizeObj(body))
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

// actionQuestions are the questions an action asks: its template's survey,
// password defaults masked.
func actionQuestions(c *Ctx, tpl int64) []Obj {
	out := []Obj{}
	for _, q := range surveyQuestions(enabledSurvey(c, tpl)) {
		if q["value_from"] != nil {
			continue
		}
		o := Obj{}
		for k, v := range q {
			if k != "choices_from" {
				o[k] = v
			}
		}
		o["dynamic"] = q["choices_from"] != nil
		if q["type"] == "password" {
			if s, _ := q["default"].(string); s != "" {
				o["default"] = crypto.Placeholder
			}
		}
		out = append(out, o)
	}
	return out
}

// RequestDeploymentAction requests one of the item's actions on a
// deployment: its template runs with the deployment's outputs and the
// answers to its survey, once the item's rules let it.
func RequestDeploymentAction(c *Ctx, d, item Obj, body Obj) (Obj, error) {
	if !ownsDeployment(c, d) {
		return nil, api.Detail(403, "Only the deployment's owners run its actions.")
	}
	fe := api.FieldErrors{}
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if !actionRequestKeys[k] {
			fe.Add(k, "Not a field of an action's request: action, answers, reason.")
		}
	}
	name, _ := body["action"].(string)
	act, found := findAction(item, name)
	if !found {
		fe.Add("action", fmt.Sprintf("The item has no action %q.", name))
	}
	answers := map[string]any{}
	if v, ok := body["answers"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			fe.Add("answers", "Expected an object of answers by variable.")
		}
		answers = m
	}
	reason, isStr := body["reason"].(string)
	if body["reason"] != nil && !isStr {
		fe.Add("reason", "Not a valid string.")
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	if err := deploymentReady(c, d, deploymentActive); err != nil {
		return nil, err
	}
	_, tpl, err := loadTemplate(c, act.Template)
	if err != nil {
		return nil, api.BadRequest("The action's template no longer exists: ask the item's owners.")
	}
	spec := enabledSurvey(c, act.Template)
	known := surveyVariables(spec)
	for _, k := range slices.Sorted(maps.Keys(answers)) {
		if !known[k] {
			fe.Add("answers", fmt.Sprintf("%s is not a question of this action.", k))
		}
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	var id int64
	if err := c.DB().QueryRow(c, "SELECT nextval(pg_get_serial_sequence('catalog_requests', 'id'))").Scan(&id); err != nil {
		return nil, err
	}
	created := time.Now().UTC().Truncate(time.Microsecond)
	inv, _ := api.ID(tpl["inventory"])
	sc := SurveyContext{Template: tpl, Inventory: inv, System: true, Labels: map[string]any{}, Picked: map[string]any{},
		Request: requestEnv(id, created, nil), Team: teamEnv(c, d["team"])}
	sv, pw, errs := ApplySurvey(c, spec, answers, sc)
	if len(errs) > 0 {
		return nil, &api.Error{Status: 400, Body: Obj{"variables_needed_to_start": errs}}
	}
	shown, keep, labels := map[string]any{}, map[string]bool{}, map[string]any{}
	for k, v := range answers {
		if _, ok := sv[k]; ok {
			shown[k], keep[k] = v, true
			if contains(pw, k) {
				shown[k] = crypto.Placeholder
			}
			if l, ok := sc.Labels[k]; ok {
				labels[k] = l
			}
		}
	}
	answered := maps.Clone(sv)
	for _, k := range pw {
		delete(answered, k)
	}
	outs, _ := d["outputs"].(map[string]any)
	vars, passwords := withAnswers(outs, sv, pw)
	p := &pendingRequest{
		ci: &catalogItem{item: item}, kind: act.ID, deployment: d["id"], statuses: []string{deploymentActive},
		team: d["team"], reason: reason, shown: shown, labels: labels,
		launch: requestLaunch{Vars: vars, Passwords: passwords, ActivityVars: keep, Template: act.Template},
		env:    &ruleEnv{answers: answered, picked: sc.Picked, team: sc.Team, user: userEnv(c, c.User.UserID), request: sc.Request, kind: act.ID},
		what:   act.Name + " on " + str(d["name"]),
	}
	req, approvals, err := p.submit(c, id, created)
	if err != nil {
		return nil, err
	}
	announceApprovals(c, p.ci, id, approvals)
	return req, nil
}

var handOverKeys = map[string]bool{"user": true, "team": true}

func handleHandOver(c *Ctx, w http.ResponseWriter, r *http.Request, d Obj) {
	if err := handOver(c, w, r, d); err != nil {
		api.WriteError(w, err)
	}
}

// handOver gives a deployment to a user, a team, or both: whoever gets it
// must be able to request its item. A team alone takes it from its owner.
func handOver(c *Ctx, w http.ResponseWriter, r *http.Request, d Obj) error {
	admin, err := deploymentAdmin(c, d)
	if err != nil {
		return err
	}
	if !ownsDeployment(c, d) && !admin {
		return api.Detail(403, "Only the deployment's owners and its item's admins hand it over.")
	}
	if d["status"] != deploymentActive {
		return api.BadRequest("Only an active deployment is handed over.")
	}
	body, err := api.ReadBody(r)
	if err != nil {
		return err
	}
	body = normalizeObj(body)
	fe := api.FieldErrors{}
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if !handOverKeys[k] {
			fe.Add(k, "Not a field of a hand-over: user, team.")
		}
	}
	_, userGiven := body["user"]
	_, teamGiven := body["team"]
	if !userGiven && !teamGiven {
		fe.Add("user", "Say whom to hand it over to: a user, a team, or both.")
	}
	if err := fe.Err(); err != nil {
		return err
	}
	item, err := systemItem(c, d)
	if err != nil {
		return err
	}
	owner, team := d["owner"], d["team"]
	if userGiven {
		owner = nil
		if body["user"] != nil {
			uid, ok := api.ID(body["user"])
			if !ok {
				return api.FieldError("user", "Expected a user's id.")
			}
			if err := userCanRequest(c, uid, item); err != nil {
				return api.FieldError("user", err.Error())
			}
			owner = uid
		}
	} else if teamGiven {
		owner = nil
	}
	if teamGiven {
		team = nil
		if body["team"] != nil {
			tid, ok := api.ID(body["team"])
			if !ok {
				return api.FieldError("team", "Expected a team's id.")
			}
			if err := teamCanRequest(c, tid, item); err != nil {
				return api.FieldError("team", err.Error())
			}
			team = tid
		}
	}
	if owner == nil && team == nil {
		return api.FieldError("user", "A deployment keeps an owner: a user, a team, or both.")
	}
	changes := Obj{}
	if !sameValue(owner, d["owner"]) {
		changes["owner"] = []any{d["owner"], owner}
	}
	if !sameValue(team, d["team"]) {
		changes["team"] = []any{d["team"], team}
	}
	if len(changes) == 0 {
		return api.BadRequest("They already own it.")
	}
	res := api.Lookup("deployments")
	var updated Obj
	err = c.WithTx(func(c *Ctx) error {
		if _, err := lockDeployment(c, int64Of(d["id"])); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE deployments SET owner_id = $2, team_id = $3, modified = now(), modified_by_id = $4 WHERE id = $1",
			d["id"], owner, team, nullID(c.User.UserID)); err != nil {
			return err
		}
		sys := *c
		sys.User = rbac.System()
		if updated, err = api.Get(&sys, res, int64Of(d["id"])); err != nil {
			return err
		}
		return api.Activity(c, "update", res, updated, changes, nil, nil, "")
	})
	if err != nil {
		return err
	}
	var to []int64
	whom := ""
	if uid, ok := api.ID(owner); ok && changes["owner"] != nil {
		to = []int64{uid}
		c.DB().QueryRow(c, "SELECT username FROM users WHERE id = $1", uid).Scan(&whom)
	} else if tid, ok := api.ID(team); ok {
		to, _ = permHolders(c, "team", tid, "member_team")
		c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", tid).Scan(&whom)
		whom = "team " + whom
	}
	to = slices.DeleteFunc(to, func(id int64) bool { return id == c.User.UserID })
	subject := fmt.Sprintf("%s is handed over to you", str(d["name"]))
	text := fmt.Sprintf("%s handed over the deployment %s (%s) to %s.\n\n%s\n", c.User.Username, str(d["name"]), str(d["item_name"]), whom, deploymentURL(c.App, d["id"]))
	mailUsers(c, item["notification_template"], nil, to, subject, text)
	out, err := api.SerializeOne(c, res, updated)
	if err != nil {
		return err
	}
	api.WriteJSON(w, 200, out)
	return nil
}

func int64Of(v any) int64 {
	id, _ := api.ID(v)
	return id
}

func deploymentURL(app *api.App, id any) string {
	return fmt.Sprintf("%s/portal/deployments/%d", strings.TrimRight(app.Cfg.BaseURL, "/"), int64Of(id))
}

// userCanRequest says why a user can't be given a deployment of the item.
func userCanRequest(c *Ctx, uid int64, item Obj) error {
	p, err := auth.LoadPrincipal(c, c.App, uid)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("No active user has this id.")
	}
	rc := *c
	rc.User = p
	ok, err := api.HasPerm(&rc, api.Lookup("catalog_items"), item, "request")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("This user can't request the item: give them a requester role on it first.")
	}
	return nil
}

// teamCanRequest says why a team can't be given a deployment of the item:
// the team, or a team it is in, must have a requester role on the item or
// its organization.
func teamCanRequest(c *Ctx, tid int64, item Obj) error {
	var exists, ok bool
	err := c.DB().QueryRow(c, `WITH RECURSIVE parents(team) AS (
			SELECT id FROM teams WHERE id = $1
			UNION
			SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id JOIN parents p ON ra.team_id = p.team
			WHERE ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions))
		SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1), EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE ra.team_id IN (SELECT team FROM parents) AND 'request_catalogitem' = ANY(rd.permissions)
			AND ((ra.content_type = 'catalogitem' AND ra.object_id = $2) OR (ra.content_type = 'organization' AND ra.object_id = $3)))`,
		tid, item["id"], item["organization"]).Scan(&exists, &ok)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("No team has this id.")
	}
	if !ok {
		return errors.New("This team can't request the item: give it a requester role on it first.")
	}
	return nil
}

func handleReveal(c *Ctx, w http.ResponseWriter, r *http.Request, d Obj) {
	out, err := reveal(c, r, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, out)
}

// reveal shows one entry of how to connect, secrets in clear, to the
// deployment's owners. Each showing is logged, without the value.
func reveal(c *Ctx, r *http.Request, d Obj) (Obj, error) {
	if !ownsDeployment(c, d) {
		return nil, api.Detail(403, "Only the deployment's owners see its secrets.")
	}
	if d["status"] == deploymentDeleted {
		return nil, api.BadRequest("The deployment is deleted.")
	}
	body, err := api.ReadBody(r)
	if err != nil {
		return nil, err
	}
	label, _ := body["label"].(string)
	outs, _ := d["outputs"].(map[string]any)
	var entry *connectionEntry
	for _, e := range layoutOf(itemLayout(c, d["catalog_item"], map[int64][]connectionEntry{}), outs) {
		if e.Label == label {
			entry = &e
			break
		}
	}
	if entry == nil {
		return nil, api.FieldError("label", fmt.Sprintf("No entry of how to connect is labelled %q.", label))
	}
	plain := maps.Clone(outs)
	for k, v := range plain {
		if s, _ := v.(string); crypto.IsEncrypted(s) {
			if plain[k], err = c.App.Crypto.Decrypt(s); err != nil {
				return nil, api.Detail(500, fmt.Sprintf("The output %s can't be decrypted.", k))
			}
		}
	}
	v, err := evalExpr(entry.Value, connectionEnv(d, plain))
	if err != nil {
		return nil, api.BadRequest(fmt.Sprintf("%s: %v", entry.Label, err))
	}
	res := api.Lookup("deployments")
	if err := c.WithTx(func(c *Ctx) error {
		return api.Activity(c, "update", res, d, Obj{"revealed": entry.Label}, nil, nil, "")
	}); err != nil {
		return nil, err
	}
	slog.Info("deployment connection shown", "deployment", d["id"], "label", entry.Label, "user", c.User.Username)
	return Obj{"label": entry.Label, "value": displayValue(v)}, nil
}

// deploymentJobFinished acts on the end of a request's job: a provisioning
// that succeeded creates the deployment, an action that succeeded updates
// its outputs, and a teardown deletes it or flags it.
func deploymentJobFinished(ctx context.Context, app *api.App, job jobs.Row) {
	if !job.Has("catalog_request_id") || job.Str("type") == "workflow_approval" {
		return
	}
	c := &Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}
	request := job.Int("catalog_request_id")
	var kind, itemKind, itemName, nameExpr string
	var dep, item, requester, team, lifetime *int64
	var org, jobID int64
	var created time.Time
	var answers map[string]any
	err := c.DB().QueryRow(c, `SELECT q.kind, q.deployment_id, q.catalog_item_id, coalesce(i.kind, ''), q.created_by_id, q.team_id, q.lifetime_days,
			q.created, q.item_name, q.organization_id, coalesce(i.deployment_name, ''), q.answers, coalesce(q.unified_job_id, 0)
		FROM catalog_requests q LEFT JOIN catalog_items i ON i.id = q.catalog_item_id WHERE q.id = $1`, request).
		Scan(&kind, &dep, &item, &itemKind, &requester, &team, &lifetime, &created, &itemName, &org, &nameExpr, &answers, &jobID)
	if err != nil || jobID != job.ID() {
		return
	}
	ok := job.Str("status") == "successful"
	workflow := job.Str("type") == "workflow_job"
	switch {
	case kind == requestNew:
		if ok && itemKind == itemDeployment {
			err = createDeployment(c, request, item, requester, team, lifetime, created, itemName, org, nameExpr, answers, jobOutputs(c, job.ID(), workflow))
		}
	case dep == nil:
	case kind == requestDelete:
		err = teardownFinished(c, *dep, job, workflow, ok)
	case ok:
		err = mergeOutputs(c, *dep, jobOutputs(c, job.ID(), workflow))
	}
	if err != nil {
		slog.Warn("a deployment's request finished", "catalog_request", request, "err", err)
	}
}

func createDeployment(c *Ctx, request int64, item, requester, team, lifetime *int64, created time.Time, itemName string, org int64,
	nameExpr string, answers, outs map[string]any) error {
	var expires any
	if lifetime != nil {
		expires = created.Add(time.Duration(*lifetime) * 24 * time.Hour)
	}
	outsJSON, _ := json.Marshal(outs)
	name := deploymentName(nameExpr, itemName, request, answers, outs)
	return c.WithTx(func(c *Ctx) error {
		var id int64
		if err := c.DB().QueryRow(c, `INSERT INTO deployments (created_by_id, modified_by_id, name, catalog_item_id, item_name, organization_id, request_id,
			owner_id, team_id, outputs, expires_at) VALUES ($1, $1, $2, $3, $4, $5, $6, $1, $7, $8, $9) RETURNING id`,
			requester, name, item, itemName, org, request, team, string(outsJSON), expires).Scan(&id); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE catalog_requests SET deployment_id = $2 WHERE id = $1", request, id); err != nil {
			return err
		}
		res := api.Lookup("deployments")
		d, err := api.Get(c, res, id)
		if err != nil {
			return err
		}
		return api.Activity(c, "create", res, d, api.ActivityFields(res, d), nil, nil, "")
	})
}

func mergeOutputs(c *Ctx, id int64, outs map[string]any) error {
	if len(outs) == 0 {
		return nil
	}
	return c.WithTx(func(c *Ctx) error {
		old, err := lockDeployment(c, id)
		if err != nil {
			return err
		}
		prev, _ := old["outputs"].(map[string]any)
		merged := maps.Clone(prev)
		if merged == nil {
			merged = map[string]any{}
		}
		maps.Copy(merged, outs)
		b, _ := json.Marshal(merged)
		if _, err := c.DB().Exec(c, "UPDATE deployments SET outputs = $2, modified = now() WHERE id = $1", id, string(b)); err != nil {
			return err
		}
		return api.Activity(c, "update", api.Lookup("deployments"), old,
			Obj{"outputs": []any{redactOutputs(prev), redactOutputs(merged)}, "updated_outputs": slices.Sorted(maps.Keys(outs))}, nil, nil, "")
	})
}

// teardownFinished deletes the deployment, or flags it with why and what
// was left, and tells its owners.
func teardownFinished(c *Ctx, id int64, job jobs.Row, workflow, ok bool) error {
	left := leftBehind(c, job.ID(), workflow)
	status, why := deploymentDeleted, ""
	if !ok {
		status = deploymentDeleteFailed
		c.DB().QueryRow(c, "SELECT job_explanation FROM unified_jobs WHERE id = $1", job.ID()).Scan(&why)
		if why == "" {
			why = fmt.Sprintf("The teardown job %d failed.", job.ID())
			if job.Str("status") == "canceled" {
				why = fmt.Sprintf("The teardown job %d was canceled.", job.ID())
			}
		}
	}
	var d Obj
	err := c.WithTx(func(c *Ctx) error {
		var err error
		if d, err = lockDeployment(c, id); err != nil {
			return err
		}
		return setDeploymentStatus(c, id, status, why, left)
	})
	if err != nil {
		return err
	}
	var template any
	c.DB().QueryRow(c, "SELECT notification_template_id FROM catalog_items WHERE id = $1", d["catalog_item"]).Scan(&template)
	subject, text := fmt.Sprintf("%s is deleted", str(d["name"])), "Its teardown finished.\n"
	if !ok {
		subject, text = fmt.Sprintf("%s couldn't be deleted", str(d["name"])), why+"\n"
	}
	mailUsers(c, template, nil, deploymentOwners(c, d), subject, text+"\n"+deploymentURL(c.App, id)+"\n")
	return nil
}

// deploymentOwners are its owner and its team's members.
func deploymentOwners(c *Ctx, d Obj) []int64 {
	var out []int64
	if uid, ok := api.ID(d["owner"]); ok {
		out = append(out, uid)
	}
	if tid, ok := api.ID(d["team"]); ok {
		members, _ := permHolders(c, "team", tid, "member_team")
		for _, m := range members {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

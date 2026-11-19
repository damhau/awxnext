package resources

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/rbac"
)

// An EDA activation is a rulebook of its own: a copy of a project's
// rulebook, uploaded to the project under the activation's name and
// imported with the activation's variables and source mappings. Its id is
// that rulebook's. Turning it on turns on its connectors and puts its rules
// in auto mode, saved by whoever turns it on: the rules then run as them.
// Turning it off turns them off. Two activations of one rulebook have
// their own objects; deleting one deletes its rulebook and objects.

const edaActivationCols = `rulebook_id, source_rulebook_id, organization_id, decision_environment_id, is_enabled, restart_policy,
	restart_count, log_level, extra_var, source_mappings, credentials, restart_on_project_update, enable_persistence,
	rule_engine_credential_id, awx_token_id, k8s_service_name, log_tracking_id, rulebook_name, rulebook_hash`

type edaActivation struct {
	ID              int64
	SourceRulebook  *int64
	Org             *int64
	DE              *int64
	Enabled         bool
	RestartPolicy   string
	RestartCount    int
	LogLevel        string
	ExtraVar        *string
	SourceMappings  string
	Credentials     []int64
	RestartOnUpdate bool
	Persistence     bool
	RuleEngine      *int64
	Token           *int64
	K8sService      string
	LogTracking     string
	RulebookName    string
	RulebookHash    string
}

func (a *edaActivation) scan(row pgx.Row) error {
	return row.Scan(&a.ID, &a.SourceRulebook, &a.Org, &a.DE, &a.Enabled, &a.RestartPolicy, &a.RestartCount, &a.LogLevel, &a.ExtraVar,
		&a.SourceMappings, &a.Credentials, &a.RestartOnUpdate, &a.Persistence, &a.RuleEngine, &a.Token, &a.K8sService, &a.LogTracking,
		&a.RulebookName, &a.RulebookHash)
}

func (a *edaActivation) save(c *Ctx) error {
	if a.Credentials == nil {
		a.Credentials = []int64{}
	}
	_, err := c.DB().Exec(c, `INSERT INTO eda_activations (`+edaActivationCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (rulebook_id) DO UPDATE SET source_rulebook_id = $2, organization_id = $3, decision_environment_id = $4, is_enabled = $5,
		restart_policy = $6, restart_count = $7, log_level = $8, extra_var = $9, source_mappings = $10, credentials = $11,
		restart_on_project_update = $12, enable_persistence = $13, rule_engine_credential_id = $14, awx_token_id = $15,
		k8s_service_name = $16, log_tracking_id = $17, rulebook_name = $18, rulebook_hash = $19`,
		a.ID, a.SourceRulebook, a.Org, a.DE, a.Enabled, a.RestartPolicy, a.RestartCount, a.LogLevel, a.ExtraVar, a.SourceMappings,
		a.Credentials, a.RestartOnUpdate, a.Persistence, a.RuleEngine, a.Token, a.K8sService, a.LogTracking, a.RulebookName, a.RulebookHash)
	return err
}

func loadEDAActivations(c *Ctx, ids []int64) (map[int64]*edaActivation, error) {
	rows, err := c.DB().Query(c, "SELECT "+edaActivationCols+" FROM eda_activations WHERE rulebook_id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*edaActivation{}
	for rows.Next() {
		a := &edaActivation{}
		if err := a.scan(rows); err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

func installEDAActivations(app *api.App) {
	base := edaRoot + "activations/"
	app.Handle("GET", base, edaActivationList)
	app.Handle("POST", base, edaActivationCreate)
	app.Handle("GET", base+"{id}/", edaActivationDetail)
	app.Handle("PATCH", base+"{id}/", edaActivationUpdate)
	app.Handle("DELETE", base+"{id}/", edaActivationDelete)
	app.Handle("POST", base+"{id}/enable/", edaActivationSwitch(true))
	app.Handle("POST", base+"{id}/disable/", edaActivationSwitch(false))
	app.Handle("POST", base+"{id}/restart/", edaActivationRestart)
	app.Handle("POST", base+"{id}/copy/", edaActivationCopy)
}

// edaActivationNames are the activation's fields in awxnext's errors.
var edaActivationNames = map[string]string{"project": "project_id", "variables": "extra_var", "source_streams": "source_mappings"}

func edaActivationList(c *Ctx, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, size, err := edaPage(q)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	params := url.Values{}
	if v := q.Get("name"); v != "" {
		params.Set("name__icontains", v)
	}
	if v := q.Get("project_id"); v != "" {
		params.Set("project", v)
	}
	var de int64
	if v := q.Get("decision_environment_id"); v != "" {
		id, ok := api.ID(v)
		if !ok {
			api.WriteError(w, api.FieldError("decision_environment_id", "Enter a whole number."))
			return
		}
		de = id
	}
	lp := api.ListParams{Params: params, Page: page, PageSize: size, DefaultOrder: "t.id",
		Extra: []func(arg ArgFunc) string{func(arg ArgFunc) string {
			pred := "EXISTS (SELECT 1 FROM eda_activations a WHERE a.rulebook_id = t.id"
			if de != 0 {
				pred += " AND a.decision_environment_id = " + arg(de)
			}
			return pred + ")"
		}}}
	objs, count, err := api.List(c, api.Lookup("rulebooks"), lp)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	results, err := renderEDAActivations(c, objs)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeEDAPage(w, r, count, page, size, results)
}

func renderEDAActivations(c *Ctx, objs []Obj) ([]Obj, error) {
	ser, err := api.Serialize(c, api.Lookup("rulebooks"), objs)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, o := range ser {
		ids = append(ids, mustID(o["id"]))
	}
	acts, err := loadEDAActivations(c, ids)
	if err != nil {
		return nil, err
	}
	out := []Obj{}
	for _, o := range ser {
		a, ok := acts[mustID(o["id"])]
		if !ok {
			continue
		}
		e, err := edaActivationOut(c, o, a)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func renderEDAActivation(c *Ctx, id int64) (Obj, error) {
	rb, err := api.Get(c, api.Lookup("rulebooks"), id)
	if err != nil {
		return nil, err
	}
	out, err := renderEDAActivations(c, []Obj{rb})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, api.Detail(404, "Not found.")
	}
	return out[0], nil
}

// edaActivationStatus is an activation's status, from its connectors'.
func edaActivationStatus(c *Ctx, a *edaActivation) (string, string, error) {
	if !a.Enabled {
		return "stopped", "", nil
	}
	rows, err := c.DB().Query(c, "SELECT status, status_explanation FROM event_connectors WHERE rulebook_id = $1 ORDER BY id", a.ID)
	if err != nil {
		return "", "", err
	}
	type conn struct{ status, why string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (conn, error) {
		var x conn
		return x, r.Scan(&x.status, &x.why)
	})
	if err != nil {
		return "", "", err
	}
	if len(list) == 0 {
		return "running", "The activation has no connectors: its rules read its event streams.", nil
	}
	pick := func(status string, of ...string) (string, string, bool) {
		for _, x := range list {
			for _, s := range of {
				if x.status == s {
					return status, x.why, true
				}
			}
		}
		return "", "", false
	}
	for _, p := range []struct {
		status string
		of     []string
	}{
		{"failed", []string{connectorFailed}},
		{"starting", []string{connectorPending, connectorStarting, connectorRestarting}},
		{"running", []string{connectorRunning}},
		{"stopped", []string{connectorStopped}},
	} {
		if s, why, ok := pick(p.status, p.of...); ok {
			return s, why, nil
		}
	}
	return "completed", list[0].why, nil
}

func edaActivationOut(c *Ctx, rb Obj, a *edaActivation) (Obj, error) {
	status, msg, err := edaActivationStatus(c, a)
	if err != nil {
		return nil, err
	}
	var rules int
	if err := c.DB().QueryRow(c, "SELECT count(*) FROM event_rules WHERE rulebook_id = $1", a.ID).Scan(&rules); err != nil {
		return nil, err
	}
	var notTranslated int
	if err := c.DB().QueryRow(c, "SELECT coalesce((report->'summary'->>'not_translated')::int, 0) FROM rulebooks WHERE id = $1", a.ID).
		Scan(&notTranslated); err != nil {
		return nil, err
	}
	if notTranslated > 0 && msg == "" {
		msg = fmt.Sprintf("%d of the rulebook's rules aren't translated: see the report of rulebook %d.", notTranslated, a.ID)
	}
	creds, err := edaActivationCredentials(c, a.Credentials)
	if err != nil {
		return nil, err
	}
	streams, err := edaMappedStreams(c, a.SourceMappings)
	if err != nil {
		return nil, err
	}
	return edaCommon(rb, Obj{"name": rb["name"], "description": rb["description"], "is_enabled": a.Enabled, "status": status,
		"status_message": msg, "extra_var": a.ExtraVar, "decision_environment_id": a.DE, "project_id": rb["project"],
		"rulebook_id": a.SourceRulebook, "rulebook_name": a.RulebookName, "organization_id": a.Org, "restart_policy": a.RestartPolicy,
		"restart_count": a.RestartCount, "log_level": a.LogLevel, "current_job_id": nil, "rules_count": rules,
		"eda_credentials": creds, "k8s_service_name": a.K8sService, "source_mappings": a.SourceMappings, "event_streams": streams,
		"skip_audit_events": false, "log_tracking_id": a.LogTracking, "restart_on_project_update": a.RestartOnUpdate,
		"enable_persistence": a.Persistence, "rule_engine_credential_id": a.RuleEngine, "awx_token_id": a.Token}), nil
}

func edaActivationCredentials(c *Ctx, ids []int64) ([]Obj, error) {
	out := []Obj{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := c.DB().Query(c, `SELECT cr.id, cr.name, cr.description, coalesce(ct.managed, false), cr.credential_type_id, cr.organization_id
		FROM credentials cr LEFT JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]Obj{}
	for rows.Next() {
		var id int64
		var name, desc string
		var managed bool
		var typ, org *int64
		if err := rows.Scan(&id, &name, &desc, &managed, &typ, &org); err != nil {
			rows.Close()
			return nil, err
		}
		byID[id] = Obj{"id": id, "name": name, "description": desc, "managed": managed, "credential_type_id": typ, "organization_id": org}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if o, ok := byID[id]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// edaSourceMapping is one of EDA's source mappings.
type edaSourceMapping struct {
	SourceName      string `yaml:"source_name"`
	RulebookHash    string `yaml:"rulebook_hash"`
	EventStreamName string `yaml:"event_stream_name"`
	EventStreamID   any    `yaml:"event_stream_id"`
}

func parseSourceMappings(text string) ([]edaSourceMapping, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var list []edaSourceMapping
	if err := yaml.Unmarshal([]byte(text), &list); err != nil {
		return nil, errors.New("Enter the source mappings as a YAML list of source_name, rulebook_hash, event_stream_name and event_stream_id.")
	}
	return list, nil
}

func edaMappedStreams(c *Ctx, text string) ([]Obj, error) {
	out := []Obj{}
	list, err := parseSourceMappings(text)
	if err != nil {
		return out, nil
	}
	for _, m := range list {
		id, ok := api.ID(m.EventStreamID)
		if !ok {
			continue
		}
		var name, uuid string
		err := c.DB().QueryRow(c, "SELECT name, uuid FROM event_streams WHERE id = $1", id).Scan(&name, &uuid)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Obj{"id": id, "name": name, "uuid": uuid})
	}
	return out, nil
}

// edaActivationTarget is what an activation's rulebook is made from.
type edaActivationTarget struct {
	source   *int64
	project  int64
	org      int64
	name     string
	rulesets string
}

// edaSourceRulebook is a project's rulebook an activation is made from.
func edaSourceRulebook(c *Ctx, v any) (*edaActivationTarget, error) {
	id, ok := api.ID(v)
	if !ok {
		return nil, api.FieldError("rulebook_id", "This field is required.")
	}
	var isActivation bool
	if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM eda_activations WHERE rulebook_id = $1)", id).Scan(&isActivation); err != nil {
		return nil, err
	}
	rb, err := api.GetVisible(c, api.Lookup("rulebooks"), id)
	if errors.Is(err, api.ErrNotFound) || isActivation {
		return nil, api.FieldError("rulebook_id", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	if err != nil {
		return nil, err
	}
	org, _ := api.ID(rb["organization"])
	return &edaActivationTarget{source: &id, project: mustID(rb["project"]), org: org, name: str(rb["name"]), rulesets: str(rb["rulesets"])}, nil
}

var (
	edaRestartPolicies = map[string]bool{restartAlways: true, restartOnFailure: true, restartNever: true}
	edaLogLevels       = map[string]bool{"debug": true, "info": true, "error": true}
)

// edaActivationIn reads an activation's fields from body onto a, and
// what its rulebook becomes: target, variables and source streams.
// old is the activation's rulebook when it exists; preset, the rulebook
// a copy is made of.
func edaActivationIn(c *Ctx, body Obj, a *edaActivation, old Obj, preset *edaActivationTarget) (*edaActivationTarget, Obj, error) {
	fe := api.FieldErrors{}
	target := preset
	if v, ok := body["rulebook_id"]; target == nil && (ok || old == nil) {
		t, err := edaSourceRulebook(c, v)
		if err != nil {
			return nil, nil, err
		}
		if old != nil && t.project != mustID(old["project"]) {
			return nil, nil, api.FieldError("rulebook_id", "An activation can't move to another project's rulebook: create another activation.")
		}
		target = t
	} else if target == nil {
		target = &edaActivationTarget{source: a.SourceRulebook, project: mustID(old["project"]), org: mustID(old["organization"]),
			name: a.RulebookName, rulesets: str(old["rulesets"])}
	}
	data := Obj{}
	name := ""
	if old != nil {
		name = str(old["name"])
	}
	if v, ok := body["name"]; ok {
		name = strings.TrimSpace(str(v))
		data["name"] = name
	}
	if name == "" {
		fe.Add("name", "This field is required.")
	}
	if v, ok := body["description"]; ok {
		data["description"] = str(v)
	}
	if v, ok := body["organization_id"]; ok && v != nil {
		if id, ok := api.ID(v); !ok || id != target.org {
			fe.Add("organization_id", "An activation is in its project's organization.")
		}
	}
	a.Org = &target.org
	if v, ok := body["decision_environment_id"]; ok || old == nil {
		id, ok := api.ID(v)
		if !ok {
			fe.Add("decision_environment_id", "This field is required.")
		} else if err := requireVisibleID(c, "decision_environment_id", "execution_environments", id); err != nil {
			return nil, nil, err
		} else {
			a.DE = &id
		}
	}
	if v, ok := body["eda_credentials"]; ok {
		list, isList := v.([]any)
		if !isList && v != nil {
			fe.Add("eda_credentials", "Expected a list of credential ids.")
		}
		ids := []int64{}
		for _, e := range list {
			id, ok := api.ID(e)
			if !ok {
				fe.Add("eda_credentials", fmt.Sprintf("Invalid credential id %v.", e))
				continue
			}
			ids = append(ids, id)
		}
		before := map[int64]bool{}
		for _, id := range a.Credentials {
			before[id] = true
		}
		for _, id := range ids {
			if before[id] {
				continue
			}
			if err := api.RequirePermID(c, "eda_credentials", "credentials", id, "use"); err != nil {
				return nil, nil, err
			}
		}
		a.Credentials = ids
	}
	if v, ok := body["extra_var"]; ok {
		if v == nil || str(v) == "" {
			a.ExtraVar = nil
		} else {
			s, isStr := v.(string)
			if !isStr {
				fe.Add("extra_var", "Enter the variables as YAML or JSON text.")
			} else {
				a.ExtraVar = &s
			}
		}
	}
	vars := Obj{}
	if a.ExtraVar != nil {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(*a.ExtraVar), &m); err != nil {
			fe.Add("extra_var", "Extra var is not in object format")
		} else if m != nil {
			vars = m
		}
	}
	data["variables"] = vars
	if v, ok := body["restart_policy"]; ok {
		if !edaRestartPolicies[str(v)] {
			fe.Add("restart_policy", fmt.Sprintf("\"%s\" is not a valid choice.", str(v)))
		}
		a.RestartPolicy = str(v)
	}
	if v, ok := body["log_level"]; ok {
		if !edaLogLevels[str(v)] {
			fe.Add("log_level", fmt.Sprintf("\"%s\" is not a valid choice.", str(v)))
		}
		a.LogLevel = str(v)
	}
	if v, ok := body["is_enabled"]; ok {
		a.Enabled = boolv(v)
	}
	if v, ok := body["restart_on_project_update"]; ok {
		a.RestartOnUpdate = boolv(v)
	}
	if v, ok := body["enable_persistence"]; ok {
		a.Persistence = boolv(v)
	}
	if v, ok := body["k8s_service_name"]; ok {
		a.K8sService = str(v)
	}
	if v, ok := body["rule_engine_credential_id"]; ok {
		a.RuleEngine = nil
		if v != nil {
			id, ok := api.ID(v)
			var ns string
			err := c.DB().QueryRow(c, `SELECT coalesce(ct.namespace, '') FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id
				WHERE cr.id = $1`, id).Scan(&ns)
			switch {
			case !ok || errors.Is(err, pgx.ErrNoRows):
				fe.Add("rule_engine_credential_id", fmt.Sprintf("Invalid pk \"%v\" - object does not exist.", v))
			case err != nil:
				return nil, nil, err
			case ns != "eda_rule_engine":
				fe.Add("rule_engine_credential_id", "The credential must be of the Event-Driven Ansible Rule Engine type.")
			default:
				if err := api.RequirePermID(c, "rule_engine_credential_id", "credentials", id, "use"); err != nil {
					return nil, nil, err
				}
				a.RuleEngine = &id
			}
		}
	}
	if v, ok := body["awx_token_id"]; ok {
		a.Token = nil
		if v != nil {
			id, ok := api.ID(v)
			var mine bool
			if ok {
				if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM eda_controller_tokens WHERE id = $1 AND user_id = $2)", id, c.User.UserID).
					Scan(&mine); err != nil {
					return nil, nil, err
				}
			}
			if !mine {
				fe.Add("awx_token_id", fmt.Sprintf("Invalid pk \"%v\" - object does not exist.", v))
			} else {
				a.Token = &id
			}
		}
	}
	streams := Obj{}
	if v, ok := body["source_mappings"]; ok {
		a.SourceMappings = str(v)
	}
	list, err := parseSourceMappings(a.SourceMappings)
	if err != nil {
		fe.Add("source_mappings", err.Error())
	}
	if len(list) > 0 {
		sources, err := edaSourceList(target.rulesets)
		if err != nil {
			fe.Add("source_mappings", "The rulebook can't be read: "+err.Error()+".")
		}
		names := map[string]bool{}
		for _, s := range sources {
			names[str(s["name"])] = true
		}
		hash := rulebookHash(target.rulesets)
		for _, m := range list {
			id, ok := api.ID(m.EventStreamID)
			switch {
			case !names[m.SourceName]:
				fe.Add("source_mappings", fmt.Sprintf("The source %s does not exist in the rulebook.", m.SourceName))
			case m.RulebookHash != hash:
				fe.Add("source_mappings", "The rulebook has changed since its sources were listed: list them again.")
			case !ok:
				fe.Add("source_mappings", fmt.Sprintf("Invalid event stream id %v.", m.EventStreamID))
			case streams[m.SourceName] != nil:
				fe.Add("source_mappings", fmt.Sprintf("The source %s is mapped twice.", m.SourceName))
			default:
				if err := requireVisibleID(c, "source_mappings", "event_streams", id); err != nil {
					return nil, nil, err
				}
				streams[m.SourceName] = id
			}
		}
	}
	data["source_streams"] = streams
	if err := fe.Err(); err != nil {
		return nil, nil, err
	}
	var taken bool
	if err := c.DB().QueryRow(c, `SELECT EXISTS (SELECT 1 FROM eda_activations a JOIN rulebooks r ON r.id = a.rulebook_id
		WHERE r.name = $1 AND a.organization_id = $2 AND a.rulebook_id <> $3)`, name, target.org, a.ID).Scan(&taken); err != nil {
		return nil, nil, err
	}
	if taken {
		return nil, nil, api.FieldError("name", fmt.Sprintf("Activation with name %s already exists.", name))
	}
	if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM rulebooks WHERE project_id = $1 AND name = $2 AND id <> $3)",
		target.project, name, a.ID).Scan(&taken); err != nil {
		return nil, nil, err
	}
	if taken {
		return nil, nil, api.FieldError("name", "The project has a rulebook of this name: give the activation another.")
	}
	return target, data, nil
}

// requireVisibleID is a field error when id isn't an object of resName
// that c's user can see.
func requireVisibleID(c *Ctx, field, resName string, id int64) error {
	var exists bool
	res := api.Lookup(resName)
	if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM "+res.Table+" WHERE id = $1)", id).Scan(&exists); err != nil {
		return err
	}
	ok := false
	if exists {
		var err error
		if ok, err = api.CanView(c, res, id); err != nil {
			return err
		}
	}
	if !ok {
		return api.FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	return nil
}

// saveEDAActivation creates or changes an activation from body, as c's
// user, and returns its id.
func saveEDAActivation(c *Ctx, body Obj, a *edaActivation, old Obj, preset *edaActivationTarget) (int64, error) {
	target, data, err := edaActivationIn(c, body, a, old, preset)
	if err != nil {
		return 0, err
	}
	res := api.Lookup("rulebooks")
	data["rulesets"] = target.rulesets
	if old == nil {
		data["project"] = target.project
		obj, err := api.Create(c, res, data, nil)
		if err != nil {
			return 0, err
		}
		a.ID = mustID(obj["id"])
		a.LogTracking = edaUUID()
	} else {
		renamed := str(data["name"]) != "" && str(data["name"]) != str(old["name"])
		obj, err := api.Update(c, res, old, data)
		if err != nil {
			return 0, err
		}
		if renamed && sameValue(old["rulesets"], obj["rulesets"]) && sameValue(old["variables"], obj["variables"]) &&
			sameValue(old["source_streams"], obj["source_streams"]) {
			if err := ImportRulebook(c, a.ID, c.User); err != nil {
				return 0, err
			}
		}
	}
	a.SourceRulebook = target.source
	a.RulebookName = target.name
	a.RulebookHash = rulebookHash(target.rulesets)
	if err := a.save(c); err != nil {
		return 0, err
	}
	return a.ID, applyEDAActivation(c, a)
}

func edaUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// applyEDAActivation gives an activation's connectors and rules its
// settings, as c's user: on, its connectors run and its rules are in auto
// mode, run as that user; off, both are off.
func applyEDAActivation(c *Ctx, a *edaActivation) error {
	connectors := api.Lookup("event_connectors")
	have, err := importedObjects(c, connectors, a.ID)
	if err != nil {
		return err
	}
	var de any
	if a.DE != nil {
		de = *a.DE
	}
	creds := a.Credentials
	if creds == nil {
		creds = []int64{}
	}
	for _, old := range have {
		want := Obj{"enabled": a.Enabled, "credentials": creds, "execution_environment": de, "restart_policy": a.RestartPolicy}
		data := Obj{}
		for k, v := range want {
			if !sameValue(old[k], v) {
				data[k] = v
			}
		}
		if len(data) == 0 {
			continue
		}
		if _, err := api.Update(c, connectors, old, data); err != nil {
			return activationRefusal(c, err, "event connector", old, "EventConnector Admin")
		}
	}
	rules := api.Lookup("event_rules")
	haveRules, err := importedObjects(c, rules, a.ID)
	if err != nil {
		return err
	}
	for _, old := range haveRules {
		data := Obj{}
		if boolv(old["enabled"]) != a.Enabled {
			data["enabled"] = a.Enabled
		}
		if a.Enabled && str(old["mode"]) != modeAuto {
			data["mode"] = modeAuto
		}
		if len(data) == 0 {
			continue
		}
		if _, err := api.Update(c, rules, old, data); err != nil {
			role := "EventRule Admin"
			if a.Enabled {
				role = "EventRule Automate"
			}
			return activationRefusal(c, fmt.Errorf("rule %s: %w", str(old["name"]), err), "event rule", old, role)
		}
	}
	return nil
}

// activationRefusal replaces the bare 403 of a connector or rule the
// activation's user may not change: the import made them, so having a
// role on the rulebook gives none on them.
func activationRefusal(c *Ctx, err error, what string, obj Obj, role string) error {
	if !errors.Is(err, api.ErrForbidden) {
		return err
	}
	return api.Detail(403, fmt.Sprintf("%s may not change %s %q, which the activation runs: that takes the %s role on it, or Organization Admin.",
		c.User.Username, what, str(obj["name"]), role))
}

// edaActivationObject is the request's activation: 404 when it isn't
// one, 403 when its project can't be seen.
func edaActivationObject(c *Ctx, r *http.Request) (Obj, *edaActivation, error) {
	id, err := edaID(r)
	if err != nil {
		return nil, nil, err
	}
	a := &edaActivation{}
	err = a.scan(c.DB().QueryRow(c, "SELECT "+edaActivationCols+" FROM eda_activations WHERE rulebook_id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, api.Detail(404, "Not found.")
	}
	if err != nil {
		return nil, nil, err
	}
	rb, err := api.GetVisible(c, api.Lookup("rulebooks"), id)
	if errors.Is(err, api.ErrNotFound) {
		return nil, nil, api.Detail(404, "Not found.")
	}
	return rb, a, err
}

func requireEDAChange(c *Ctx, rb Obj) error {
	ok, err := api.HasPerm(c, api.Lookup("rulebooks"), rb, "change")
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

func edaActivationDetail(c *Ctx, w http.ResponseWriter, r *http.Request) {
	rb, _, err := edaActivationObject(c, r)
	var out Obj
	if err == nil {
		var list []Obj
		list, err = renderEDAActivations(c, []Obj{rb})
		if err == nil && len(list) > 0 {
			out = list[0]
		}
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, out)
}

func edaActivationCreate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	a := &edaActivation{RestartPolicy: restartOnFailure, LogLevel: "error", Enabled: true}
	writeEDAActivation(c, w, 201, func(c *Ctx) (int64, error) { return saveEDAActivation(c, body, a, nil, nil) })
}

func writeEDAActivation(c *Ctx, w http.ResponseWriter, status int, fn func(c *Ctx) (int64, error)) {
	var out Obj
	err := c.WithTx(func(c *Ctx) error {
		id, err := fn(c)
		if err != nil {
			return err
		}
		out, err = renderEDAActivation(c, id)
		return err
	})
	if err != nil {
		writeEDAError(w, err, edaActivationNames)
		return
	}
	api.WriteJSON(w, status, out)
}

func edaActivationUpdate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	rb, a, err := edaActivationObject(c, r)
	if err == nil {
		err = requireEDAChange(c, rb)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeEDAActivation(c, w, 200, func(c *Ctx) (int64, error) { return saveEDAActivation(c, body, a, rb, nil) })
}

func edaActivationDelete(c *Ctx, w http.ResponseWriter, r *http.Request) {
	rb, _, err := edaActivationObject(c, r)
	if err == nil {
		err = c.WithTx(func(c *Ctx) error { return api.Delete(c, api.Lookup("rulebooks"), rb) })
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func edaActivationSwitch(on bool) func(c *Ctx, w http.ResponseWriter, r *http.Request) {
	return func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		rb, a, err := edaActivationObject(c, r)
		if err == nil {
			err = requireEDAChange(c, rb)
		}
		if err == nil {
			err = c.WithTx(func(c *Ctx) error {
				a.Enabled = on
				if err := a.save(c); err != nil {
					return err
				}
				return applyEDAActivation(c, a)
			})
		}
		if err != nil {
			api.WriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// edaActivationRestart starts the activation's connectors again, as each
// connector's restart/ does, and counts the restart.
func edaActivationRestart(c *Ctx, w http.ResponseWriter, r *http.Request) {
	rb, a, err := edaActivationObject(c, r)
	if err == nil {
		err = requireEDAChange(c, rb)
	}
	if err == nil && !a.Enabled {
		err = api.BadRequest("Activation is not enabled: enable it to restart it.")
	}
	if err == nil {
		err = c.WithTx(func(c *Ctx) error { return restartEDAActivation(c, a.ID, "Restarted by a user.") })
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func restartEDAActivation(c *Ctx, id int64, why string) error {
	if _, err := c.DB().Exec(c, `UPDATE event_connectors SET status = $2, status_explanation = $3, controller_node = '',
		execution_node = '', token_hash = '', next_start = NULL, restart_count = 0 WHERE rulebook_id = $1 AND enabled`,
		id, connectorPending, why); err != nil {
		return err
	}
	_, err := c.DB().Exec(c, "UPDATE eda_activations SET restart_count = restart_count + 1 WHERE rulebook_id = $1 AND is_enabled", id)
	return err
}

// edaActivationCopy copies an activation, turned off, with the rulebook
// it was made with.
func edaActivationCopy(c *Ctx, w http.ResponseWriter, r *http.Request) {
	rb, a, err := edaActivationObject(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	name := strings.TrimSpace(str(body["name"]))
	if name == "" {
		api.WriteError(w, api.FieldError("name", "This field is required."))
		return
	}
	writeEDAActivation(c, w, 201, func(c *Ctx) (int64, error) {
		if err := requireEDAChange(c, rb); err != nil {
			return 0, err
		}
		in := Obj{"name": name, "description": rb["description"], "eda_credentials": edaIDList(a.Credentials),
			"source_mappings": a.SourceMappings, "is_enabled": false}
		if a.DE != nil {
			in["decision_environment_id"] = *a.DE
		}
		if a.ExtraVar != nil {
			in["extra_var"] = *a.ExtraVar
		}
		copied := &edaActivation{RestartPolicy: a.RestartPolicy, LogLevel: a.LogLevel, RestartOnUpdate: a.RestartOnUpdate,
			Persistence: a.Persistence, RuleEngine: a.RuleEngine, Token: a.Token, K8sService: a.K8sService}
		// The copy runs the rulebook the activation runs, not the project's
		// rulebook as it may be now.
		target := &edaActivationTarget{source: a.SourceRulebook, project: mustID(rb["project"]), org: mustID(rb["organization"]),
			name: a.RulebookName, rulesets: str(rb["rulesets"])}
		return saveEDAActivation(c, in, copied, nil, target)
	})
}

func edaIDList(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// RefreshEDAActivations gives the activations of a project's rulebooks
// that restart on a project update the rulebooks' new text, imports them
// again and restarts those turned on. viewer looks templates up.
func RefreshEDAActivations(c *Ctx, project int64, viewer *rbac.Principal) error {
	rows, err := c.DB().Query(c, `SELECT a.rulebook_id, s.rulesets, coalesce(i.modified_by_id, i.created_by_id, 0) FROM eda_activations a
		JOIN rulebooks s ON s.id = a.source_rulebook_id JOIN rulebooks i ON i.id = a.rulebook_id
		WHERE s.project_id = $1 AND a.restart_on_project_update AND i.rulesets <> s.rulesets ORDER BY a.rulebook_id`, project)
	if err != nil {
		return err
	}
	type stale struct {
		id, user int64
		text     string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stale, error) {
		var x stale
		return x, r.Scan(&x.id, &x.text, &x.user)
	})
	if err != nil {
		return err
	}
	for _, s := range list {
		if _, err := c.DB().Exec(c, "UPDATE rulebooks SET rulesets = $2, modified = now() WHERE id = $1", s.id, s.text); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE eda_activations SET rulebook_hash = $2 WHERE rulebook_id = $1", s.id, rulebookHash(s.text)); err != nil {
			return err
		}
		if err := ImportRulebook(c, s.id, viewer); err != nil {
			return err
		}
		acts, err := loadEDAActivations(c, []int64{s.id})
		if err != nil {
			return err
		}
		a := acts[s.id]
		// New connectors and rules start off: they are turned on as who last
		// saved the activation, as its other rules run.
		if a != nil && a.Enabled && s.user != 0 {
			p, err := auth.LoadPrincipal(c, c.App, s.user)
			if err != nil {
				return err
			}
			if p != nil {
				uc := *c
				uc.User, uc.Extra = p, map[string]any{}
				if err := applyEDAActivation(&uc, a); err != nil {
					return err
				}
			}
		}
		if err := restartEDAActivation(c, s.id, "Restarted for its project's update."); err != nil {
			return err
		}
	}
	return nil
}

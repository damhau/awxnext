package resources

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
)

// Event connectors (awxnext, roadmap M14-06): an ansible.eda source plugin,
// such as Kafka's, run where jobs run and posting its events to an event
// stream. The runner and its placement are in connector_runs.go.

const (
	connectorPending    = "pending"
	connectorStarting   = "starting"
	connectorRunning    = "running"
	connectorRestarting = "restarting"
	connectorStopped    = "stopped"
	connectorCompleted  = "completed"
	connectorFailed     = "failed"

	restartAlways    = "always"
	restartOnFailure = "on-failure"
	restartNever     = "never"

	maxConnectorBuffer = 1_000_000
	maxConnectorDelay  = 3600
)

var pluginNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
var pythonRe = regexp.MustCompile(`^[A-Za-z0-9_./+-]{0,200}$`)

// connectorConfigFields are the fields a running connector restarts for.
var connectorConfigFields = []string{"event_stream", "source", "args", "filters", "credentials", "execution_environment", "instance_group", "python", "buffer_size"}

func registerEventConnectors() {
	res := api.Register(&Resource{
		Name:        "event_connectors",
		Type:        "event_connector",
		Table:       "event_connectors",
		Verbose:     "Event Connector",
		Ordering:    "name",
		ContentType: "eventconnector",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Managed:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "event_stream", Type: api.TFK, Ref: "event_streams", Required: true, Help: "The stream the connector posts its events to."},
			{Name: "source", Type: api.TString, Required: true, Validate: validatePluginName,
				Help: "The source plugin, by its full name, such as ansible.eda.kafka: the image must have its collection and its dependencies."},
			{Name: "args", Type: api.TJSON, Default: Obj{}, Validate: validateConnectorArgs, Help: "The source plugin's arguments."},
			{Name: "filters", Type: api.TJSON, Default: []any{}, Validate: validateConnectorFilters,
				Help: "Event filters applied in order, as a rulebook's: a list of {\"ansible.eda.json_filter\": {arguments}}."},
			{Name: "credentials", Type: api.TJSON, Default: []any{}, Validate: validateIDList("credential"),
				Help: "The ids of the credentials given to the source: their type's env injectors set its environment, extra_vars become its arguments (unless args sets them), files are written where only it can read them."},
			{Name: "execution_environment", Type: api.TFK, Ref: "execution_environments", Nullable: true,
				Help: "The image the connector runs in, such as a decision environment. Empty: the default execution environment."},
			{Name: "instance_group", Type: api.TFK, Ref: "instance_groups", Nullable: true,
				Help: "Where the connector runs: an instance group of execution or hybrid nodes, or a container group. Empty: the default group."},
			{Name: "python", Type: api.TString, Default: "", Validate: validatePython,
				Help: "The Python interpreter. Empty: ansible-rulebook's if the image has it, else python3."},
			{Name: "enabled", Type: api.TBool, Default: true},
			{Name: "restart_policy", Type: api.TChoice, Default: restartOnFailure,
				Choices: labelled(restartAlways, "Always", restartOnFailure, "On failure", restartNever, "Never"),
				Help:    "When the connector starts again after it stops: always, on failure only, or never. A connector whose node is lost moves to another node whatever its policy."},
			{Name: "restart_delay", Type: api.TInt, Default: 5, Validate: between(1, maxConnectorDelay),
				Help: "Seconds before the first restart. The delay doubles with each restart in a row, up to 64 times."},
			{Name: "max_restarts", Type: api.TInt, Default: 0, Validate: between(0, 10000),
				Help: "Restarts in a row after which the connector stays failed. 0: no limit."},
			{Name: "buffer_size", Type: api.TInt, Default: 1000, Validate: between(1, maxConnectorBuffer),
				Help: "Events the connector holds while awxnext is unreachable. When it is full, the source waits."},
			{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: labelled(connectorPending, "Pending", connectorStarting, "Starting",
				connectorRunning, "Running", connectorRestarting, "Restarting", connectorStopped, "Stopped", connectorCompleted, "Completed", connectorFailed, "Failed")},
			{Name: "status_explanation", Type: api.TText, ReadOnly: true},
			{Name: "controller_node", Type: api.TString, ReadOnly: true, Help: "The control node that started the connector and watches it."},
			{Name: "execution_node", Type: api.TString, ReadOnly: true, Help: "The node the connector runs on. Empty in a container group: its pod."},
			{Name: "started", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When the connector's current run started."},
			{Name: "next_start", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When a restarting connector starts again."},
			{Name: "restart_count", Type: api.TInt, ReadOnly: true, Help: "Restarts in a row."},
			{Name: "stats", Type: api.TJSON, ReadOnly: true,
				Help: "What the connector last reported: events sent, buffered, dropped and rejected, retries, and whether awxnext is unreachable."},
		},
		BeforeSave: saveEventConnector,
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			if connectorReset(old, obj) {
				_, err := c.DB().Exec(c, "UPDATE event_connectors SET token_hash = '' WHERE id = $1", obj["id"])
				return err
			}
			return nil
		},
		Summary:  connectorSummary,
		SubLists: []*SubList{connectorRunsSub()},
		Actions:  []*api.Action{{Name: "restart", Methods: []string{"POST"}, Perm: "change", Handler: restartConnectorView}},
	})

	api.Register(&Resource{
		Name:         "event_connector_runs",
		Type:         "event_connector_run",
		Table:        "event_connector_runs",
		Verbose:      "Event Connector Run",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-id",
		SearchFields: []string{"status", "controller_node", "execution_node"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "connector", Type: api.TFK, Ref: "event_connectors", ReadOnly: true},
			{Name: "generation", Type: api.TInt, ReadOnly: true, Help: "The connector's placement this run belongs to."},
			{Name: "controller_node", Type: api.TString, ReadOnly: true},
			{Name: "execution_node", Type: api.TString, ReadOnly: true},
			{Name: "started", Type: api.TDateTime, ReadOnly: true},
			{Name: "finished", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: labelled("running", "Running", "completed", "Completed", "failed", "Failed",
				"stopped", "Stopped", "lost", "Lost")},
			{Name: "rc", Type: api.TInt, ReadOnly: true, Nullable: true},
			{Name: "explanation", Type: api.TText, ReadOnly: true},
			{Name: "log", Type: api.TText, ReadOnly: true, Help: "The end of the run's output: the runner's log and the source plugin's."},
			{Name: "stats", Type: api.TJSON, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.connector_id IN (SELECT t.id FROM event_connectors t WHERE %s)", api.ViewPredicate(c, res, arg))
		},
	})

	creds := api.Lookup("credentials")
	prev := creds.AfterDelete
	creds.AfterDelete = func(c *Ctx, obj Obj) error {
		if prev != nil {
			if err := prev(c, obj); err != nil {
				return err
			}
		}
		_, err := c.DB().Exec(c, `UPDATE event_connectors SET credentials = (SELECT coalesce(jsonb_agg(e ORDER BY e), '[]'::jsonb)
			FROM jsonb_array_elements(credentials) e WHERE e <> to_jsonb($1::bigint)), modified = now()
			WHERE credentials @> jsonb_build_array($1::bigint)`, obj["id"])
		return err
	}
}

func connectorRunsSub() *SubList {
	return readOnlyChildSub("runs", "event_connector_runs", "connector_id")
}

func validatePluginName(c *Ctx, v any, data Obj) error {
	if !pluginNameRe.MatchString(str(v)) {
		return errors.New("Expected a plugin's full name: namespace.collection.name.")
	}
	return nil
}

func validatePython(c *Ctx, v any, data Obj) error {
	if !pythonRe.MatchString(str(v)) {
		return errors.New("Expected an interpreter's name or path, such as python3.11 or /usr/bin/python3.")
	}
	return nil
}

func validateConnectorArgs(c *Ctx, v any, data Obj) error {
	switch v.(type) {
	case map[string]any, nil:
		return nil
	}
	return errors.New("Expected an object: the source's arguments by name.")
}

func validateConnectorFilters(c *Ctx, v any, data Obj) error {
	list, ok := v.([]any)
	if v == nil {
		return nil
	}
	if !ok {
		return errors.New("Expected a list of filters, each {\"namespace.collection.name\": {arguments}}.")
	}
	if len(list) > 50 {
		return errors.New("A connector has at most 50 filters.")
	}
	for i, f := range list {
		m, ok := f.(map[string]any)
		if !ok || len(m) != 1 {
			return fmt.Errorf("Filter %d: expected {\"namespace.collection.name\": {arguments}}.", i+1)
		}
		for name, args := range m {
			if !pluginNameRe.MatchString(name) {
				return fmt.Errorf("Filter %d: %q is not a plugin's full name.", i+1, name)
			}
			if _, ok := args.(map[string]any); !ok && args != nil {
				return fmt.Errorf("Filter %d: its arguments must be an object.", i+1)
			}
		}
	}
	return nil
}

func saveEventConnector(c *Ctx, old, data Obj) error {
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	fe := api.FieldErrors{}
	if v, ok := data["event_stream"]; old == nil || ok && !sameValue(old["event_stream"], v) {
		sid, _ := api.ID(merged["event_stream"])
		org, _ := api.ID(merged["organization"])
		var streamOrg int64
		err := c.DB().QueryRow(c, "SELECT organization_id FROM event_streams WHERE id = $1", sid).Scan(&streamOrg)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			fe.Add("event_stream", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", sid))
		case err != nil:
			return err
		case streamOrg != org:
			fe.Add("event_stream", "The stream must be in the connector's organization.")
		}
	}
	if err := fe.Err(); err != nil {
		return err
	}
	// A connector posts without the stream's credential: it takes the right
	// to change the stream.
	if v, ok := data["event_stream"]; old == nil || ok && !sameValue(old["event_stream"], v) {
		if err := api.RequirePermID(c, "event_stream", "event_streams", merged["event_stream"], "change"); err != nil {
			return err
		}
	}
	if v, ok := data["credentials"]; ok {
		ids := append([]int64{}, streamIDs(v)...)
		data["credentials"] = ids
		var n int
		if err := c.DB().QueryRow(c, "SELECT count(*) FROM credentials WHERE id = ANY($1)", ids).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			return api.FieldError("credentials", "A credential of the list does not exist.")
		}
		before := map[int64]bool{}
		if old != nil {
			for _, id := range streamIDs(old["credentials"]) {
				before[id] = true
			}
		}
		for _, id := range ids {
			if before[id] {
				continue
			}
			if err := api.RequirePermID(c, "credentials", "credentials", id, "use"); err != nil {
				return err
			}
		}
	}
	if v, ok := data["instance_group"]; ok && v != nil && (old == nil || fmt.Sprint(v) != fmt.Sprint(old["instance_group"])) {
		if err := api.RequirePermID(c, "instance_group", "instance_groups", v, "use"); err != nil {
			return err
		}
	}
	if old == nil {
		data["status"] = connectorPending
		if v, ok := data["enabled"]; ok && !boolv(v) {
			data["status"] = connectorStopped
		}
		return nil
	}
	if connectorReset(old, merged) {
		resetConnectorData(data, boolv(merged["enabled"]), "Restarted to apply a change.")
	}
	return nil
}

// connectorReset reports whether a change stops the connector's run: it
// was turned on or off, or its configuration changed.
func connectorReset(old, obj Obj) bool {
	if boolv(old["enabled"]) != boolv(obj["enabled"]) {
		return true
	}
	for _, f := range connectorConfigFields {
		if v, ok := obj[f]; ok && !sameValue(old[f], v) {
			return true
		}
	}
	return false
}

func resetConnectorData(data Obj, enabled bool, why string) {
	data["controller_node"], data["execution_node"], data["next_start"], data["restart_count"] = "", "", nil, 0
	if enabled {
		data["status"], data["status_explanation"] = connectorPending, why
	} else {
		data["status"], data["status_explanation"] = connectorStopped, ""
	}
}

// restartConnectorView serves /event_connectors/N/restart/: it stops the
// connector's run, if any, and places it again now.
func restartConnectorView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	tag, err := c.DB().Exec(c, `UPDATE event_connectors SET status = $2, status_explanation = 'Restarted by a user.', controller_node = '',
		execution_node = '', token_hash = '', next_start = NULL, restart_count = 0 WHERE id = $1 AND enabled`, obj["id"], connectorPending)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		api.WriteError(w, api.BadRequest("The connector is turned off: turn it on to start it."))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func connectorSummary(c *Ctx, objs []Obj) error {
	var ids []int64
	for _, o := range objs {
		ids = append(ids, streamIDs(o["credentials"])...)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := c.DB().Query(c, `SELECT cr.id, cr.name, coalesce(ct.kind, '') FROM credentials cr
		LEFT JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	byID := map[int64]Obj{}
	for rows.Next() {
		var id int64
		var name, kind string
		if err := rows.Scan(&id, &name, &kind); err != nil {
			rows.Close()
			return err
		}
		byID[id] = Obj{"id": id, "name": name, "kind": kind}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range objs {
		list := []Obj{}
		for _, id := range streamIDs(o["credentials"]) {
			if s, ok := byID[id]; ok {
				list = append(list, s)
			}
		}
		if sf, ok := o["summary_fields"].(Obj); ok {
			sf["credentials"] = list
		}
	}
	return nil
}

// connectorScheme is the Authorization scheme of a connector's posts.
const connectorScheme = "Connector "

// connectorPoster is the connector posting to a stream with its token: its
// id, or 0 when the request carries no connector token. A token that is not
// the current one of an enabled connector of the stream is refused.
func connectorPoster(c *Ctx, r *http.Request, stream int64) (int64, bool, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, connectorScheme) {
		return 0, false, nil
	}
	tok := strings.TrimSpace(h[len(connectorScheme):])
	if tok == "" {
		return 0, true, nil
	}
	var id int64
	err := c.App.DB.QueryRow(c, `SELECT id FROM event_connectors WHERE event_stream_id = $1 AND token_hash = $2 AND enabled
		AND status IN ('starting', 'running')`, stream, agent.HashToken(tok)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, true, nil
	}
	return id, true, err
}

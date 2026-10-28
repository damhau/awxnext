package resources

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/vars"
)

// installBulk adds AWX's bulk API: several jobs launched as one workflow
// job, and many hosts created or deleted in one request, with AWX's limits
// (BULK_JOB_MAX_LAUNCH, BULK_HOST_MAX_CREATE, BULK_HOST_MAX_DELETE), checks
// and messages.
func installBulk(app *api.App) {
	app.Handle("GET", "/api/v2/bulk/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, Obj{"host_create": "/api/v2/bulk/host_create/", "host_delete": "/api/v2/bulk/host_delete/", "job_launch": "/api/v2/bulk/job_launch/"})
	})
	for _, ep := range []struct {
		path, detail, name string
		post               http.HandlerFunc
		handler            func(c *Ctx, w http.ResponseWriter, r *http.Request)
		fields             Obj
	}{
		{path: "job_launch", detail: "Specify a list of unified job templates to launch alongside their launchtime parameters", name: "Bulk Job Launch", handler: bulkJobLaunch,
			fields: Obj{"name": bulkField("string", "Name"), "jobs": bulkField("list", "Jobs"), "description": bulkField("string", "Description"),
				"extra_vars": bulkField("json", "Extra vars"), "organization": bulkField("field", "Organization"), "inventory": bulkField("field", "Inventory"),
				"limit": bulkField("string", "Limit"), "scm_branch": bulkField("string", "Scm branch"), "skip_tags": bulkField("string", "Skip tags"),
				"job_tags": bulkField("string", "Job tags")}},
		{path: "host_create", detail: "Bulk create hosts with this endpoint", name: "Bulk Host Create", handler: bulkHostCreate,
			fields: Obj{"inventory": bulkField("field", "Inventory"), "hosts": bulkField("list", "Hosts")}},
		{path: "host_delete", detail: "Bulk delete hosts with this endpoint", name: "Bulk Host Delete", handler: bulkHostDelete,
			fields: Obj{"hosts": bulkField("list", "Hosts")}},
	} {
		ep := ep
		path := "/api/v2/bulk/" + ep.path + "/"
		app.Handle("GET", path, func(c *Ctx, w http.ResponseWriter, r *http.Request) {
			api.WriteJSON(w, 200, Obj{"detail": ep.detail})
		})
		app.Handle("POST", path, ep.handler)
		app.Handle("OPTIONS", path, func(c *Ctx, w http.ResponseWriter, r *http.Request) {
			api.WriteJSON(w, 200, Obj{"name": ep.name, "description": "", "renders": []string{"application/json"}, "parses": []string{"application/json"},
				"actions": Obj{"POST": ep.fields}})
		})
	}
}

func bulkField(typ, label string) Obj { return Obj{"type": typ, "label": label, "required": false} }

// nonFieldError is a serializer's validation error that names no field.
func nonFieldError(msg string) error {
	return &api.Error{Status: 400, Body: Obj{"non_field_errors": []string{msg}}}
}

// pyRepr writes values as Python prints a list or a set of them, which AWX's
// messages contain: ['a', 'b'] or {1, 2}.
func pyRepr(open, close string, items []string, quote bool) string {
	out := make([]string, len(items))
	for i, s := range items {
		if quote {
			s = "'" + s + "'"
		}
		out[i] = s
	}
	return open + strings.Join(out, ", ") + close
}

func bulkHostCreate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body = normalizeObj(body)
	out, err := createHostsInBulk(c, body)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, out)
}

var errBulkInvalid = errors.New("invalid bulk request")

func createHostsInBulk(c *Ctx, body Obj) (Obj, error) {
	fe := api.FieldErrors{}
	invID, ok := api.ID(body["inventory"])
	if !ok {
		fe.Add("inventory", "This field is required.")
	}
	hostsIn, _ := body["hosts"].([]any)
	if body["hosts"] == nil {
		fe.Add("hosts", "This field is required.")
	} else if len(hostsIn) == 0 {
		fe.Add("hosts", "This list may not be empty.")
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	invRes, hostRes := api.Lookup("inventories"), api.Lookup("hosts")
	inv, err := api.Get(c, invRes, invID)
	if err != nil {
		return nil, api.FieldError("inventory", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", invID))
	}
	if str(inv["kind"]) != "" {
		return nil, nonFieldError("Hosts can only be created in manual inventories (not smart or constructed types).")
	}
	if len(hostsIn) > c.App.Settings.Int(c, "BULK_HOST_MAX_CREATE") {
		return nil, nonFieldError("Number of hosts exceeds system setting BULK_HOST_MAX_CREATE")
	}
	if ok, err := api.HasPerm(c, invRes, inv, "change"); err != nil {
		return nil, err
	} else if !ok {
		return nil, nonFieldError(fmt.Sprintf("Inventory with id %d not found or lack permissions to add hosts.", invID))
	}
	existing := map[string]bool{}
	rows, err := c.DB().Query(c, "SELECT name FROM hosts WHERE inventory_id = $1", invID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		existing[n] = true
	}
	rows.Close()
	count := map[string]int{}
	for _, h := range hostsIn {
		if hm, ok := h.(map[string]any); ok {
			count[str(hm["name"])]++
		}
	}
	var dups []string
	for _, h := range hostsIn {
		if hm, ok := h.(map[string]any); ok {
			if n := str(hm["name"]); existing[n] || count[n] > 1 {
				dups = append(dups, n)
			}
		}
	}
	if len(dups) > 0 {
		return nil, nonFieldError("Hostnames must be unique in an inventory. Duplicates found: " + pyRepr("[", "]", dups, true))
	}

	// Each host is created as through /hosts/, all or none.
	hostErrs := make([]any, len(hostsIn))
	for i := range hostErrs {
		hostErrs[i] = Obj{}
	}
	var created []Obj
	err = c.WithTx(func(c *Ctx) error {
		for i, h := range hostsIn {
			hm, ok := h.(map[string]any)
			if !ok {
				hostErrs[i] = Obj{"non_field_errors": []string{fmt.Sprintf("Invalid data. Expected a dictionary, but got %T.", h)}}
				return errBulkInvalid
			}
			data := Obj{"inventory": invID}
			for _, k := range []string{"name", "enabled", "instance_id", "description", "variables"} {
				if v, ok := hm[k]; ok {
					data[k] = v
				}
			}
			if m, ok := data["variables"].(map[string]any); ok {
				data["variables"] = vars.MustJSON(m)
			}
			obj, err := api.Create(c, hostRes, data, nil)
			var ae *api.Error
			if errors.As(err, &ae) && ae.Status == 400 {
				hostErrs[i] = ae.Body
				return errBulkInvalid
			}
			if err != nil {
				return err
			}
			created = append(created, obj)
		}
		return nil
	})
	if err == errBulkInvalid {
		return nil, &api.Error{Status: 400, Body: Obj{"hosts": hostErrs}}
	}
	if err != nil {
		return nil, err
	}
	invURL := invRes.URL(invID)
	out := make([]Obj, len(created))
	for i, h := range created {
		id, _ := api.ID(h["id"])
		out[i] = Obj{"name": h["name"], "enabled": h["enabled"], "instance_id": h["instance_id"], "description": h["description"],
			"variables": h["variables"], "id": id, "url": hostRes.URL(id), "inventory": invURL}
	}
	return Obj{"url": invURL, "hosts": out}, nil
}

func bulkHostDelete(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body = normalizeObj(body)
	out, err := deleteHostsInBulk(c, body)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, out)
}

func deleteHostsInBulk(c *Ctx, body Obj) (Obj, error) {
	raw, _ := body["hosts"].([]any)
	if body["hosts"] == nil {
		return nil, api.FieldError("hosts", "This field is required.")
	}
	if len(raw) == 0 {
		return nil, api.FieldError("hosts", "This list may not be empty.")
	}
	if max := c.App.Settings.Int(c, "BULK_HOST_MAX_DELETE"); len(raw) > max {
		return nil, &api.Error{Status: 400, Body: Obj{"ERROR": "Number of hosts exceeds system setting BULK_HOST_MAX_DELETE",
			"BULK_HOST_MAX_DELETE": max, "Hosts_count": len(raw)}}
	}
	var ids []int64
	for _, x := range raw {
		id, ok := api.ID(x)
		if !ok {
			return nil, api.FieldError("hosts", "A valid integer is required.")
		}
		ids = append(ids, id)
	}
	type host struct {
		id, inv    int64
		name, kind string
		invName    string
	}
	found := map[int64]host{}
	rows, err := c.DB().Query(c, `SELECT h.id, h.name, h.inventory_id, i.name, i.kind FROM hosts h JOIN inventories i ON i.id = h.inventory_id
		WHERE h.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h host
		rows.Scan(&h.id, &h.name, &h.inv, &h.invName, &h.kind)
		found[h.id] = h
	}
	rows.Close()
	missing := Obj{}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing[strconv.FormatInt(id, 10)] = "Hosts do not exist or you lack permission to delete it"
		}
	}
	if len(missing) > 0 {
		return nil, &api.Error{Status: 400, Body: Obj{"hosts": missing}}
	}
	invs := map[int64]host{}
	for _, h := range found {
		invs[h.inv] = h
	}
	denied, notManual := Obj{}, Obj{}
	for inv, h := range invs {
		if ok, err := api.HasPermID(c, "inventories", inv, "change"); err != nil {
			return nil, err
		} else if !ok {
			denied[h.invName] = "Lack permissions to delete hosts from this inventory."
		}
		if h.kind != "" {
			notManual[h.invName] = "Hosts can only be deleted from manual inventories."
		}
	}
	if len(denied) > 0 {
		return nil, &api.Error{Status: 403, Body: Obj{"inventories": denied}}
	}
	if len(notManual) > 0 {
		return nil, &api.Error{Status: 400, Body: Obj{"inventories": notManual}}
	}
	result := Obj{}
	err = c.WithTx(func(c *Ctx) error {
		if _, err := c.DB().Exec(c, "DELETE FROM hosts WHERE id = ANY($1)", ids); err != nil {
			return err
		}
		// As AWX: one entry per inventory, listing the hosts it lost.
		invRes := api.Lookup("inventories")
		for inv := range invs {
			var gone []Obj
			for _, id := range ids {
				if h := found[id]; h.inv == inv {
					gone = append(gone, Obj{"host_id": h.id, "host_name": h.name})
				}
			}
			obj, err := api.Get(c, invRes, inv)
			if err != nil {
				return err
			}
			if err := api.Activity(c, "update", invRes, obj, Obj{"deleted_hosts": gone}, nil, nil, ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[strconv.FormatInt(id, 10)] = fmt.Sprintf("The host %s was deleted", found[id].name)
	}
	return Obj{"hosts": result}, nil
}

func bulkJobLaunch(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	id, err := launchBulkJob(c, normalizeObj(body))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup("workflow_jobs")
	obj, err := api.Get(c, res, id)
	if err == nil {
		obj, err = api.SerializeOne(c, res, obj)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, obj)
}

// bulkTemplateTypes are the templates a bulk job may launch, with AWX's
// plural names for its messages.
var bulkTemplateTypes = map[string]string{"job_template": "Job Templates", "workflow_job_template": "Workflow Job Templates",
	"project": "Projects", "inventory_source": "Inventory Sources"}

// launchBulkJob creates a workflow job with one node per requested job, as
// AWX's BulkJobLaunchSerializer. A node's prompts are checked against its
// template like the prompts of a workflow template node.
func launchBulkJob(c *Ctx, body Obj) (int64, error) {
	fe := api.FieldErrors{}
	jobsIn, _ := body["jobs"].([]any)
	if body["jobs"] == nil {
		fe.Add("jobs", "This field is required.")
	} else if len(jobsIn) == 0 {
		fe.Add("jobs", "This list may not be empty.")
	}
	name := "Bulk Job Launch"
	if v, ok := body["name"]; ok {
		name = str(v)
		if len(name) > 512 {
			fe.Add("name", "Ensure this field has no more than 512 characters.")
		}
	}
	for _, f := range []string{"description", "limit", "scm_branch", "skip_tags", "job_tags"} {
		if v, ok := body[f]; ok && str(v) == "" {
			fe.Add(f, "This field may not be blank.")
		}
	}
	if err := fe.Err(); err != nil {
		return 0, err
	}
	if len(jobsIn) > c.App.Settings.Int(c, "BULK_JOB_MAX_LAUNCH") {
		return 0, nonFieldError("Number of requested jobs exceeds system setting BULK_JOB_MAX_LAUNCH")
	}

	// Each job: its template, its prompts and the objects they name.
	jobErrs := make([]any, len(jobsIn))
	bad := false
	seen := map[string]bool{}
	denied := map[string][]string{}
	type node struct {
		ujt           int64
		identifier    string
		data          Obj
		creds, labels []int64
	}
	nodes := make([]node, len(jobsIn))
	for i, j := range jobsIn {
		jobErrs[i] = Obj{}
		jm, ok := j.(map[string]any)
		if !ok {
			jobErrs[i], bad = Obj{"non_field_errors": []string{"Invalid data. Expected a dictionary."}}, true
			continue
		}
		jfe := api.FieldErrors{}
		ujt, ok := api.ID(jm["unified_job_template"])
		if !ok || ujt < 1 {
			jfe.Add("unified_job_template", "This field is required.")
		}
		n := node{ujt: ujt, data: Obj{}}
		if v, ok := jm["identifier"]; ok {
			n.identifier = str(v)
			if seen[n.identifier] {
				return 0, nonFieldError(fmt.Sprintf("Identifier %s not unique", n.identifier))
			}
			seen[n.identifier] = true
		}
		for _, f := range append([]string{"extra_data", "all_parents_must_converge"}, schedulePrompts...) {
			if v, ok := jm[f]; ok {
				n.data[f] = v
			}
		}
		if s, ok := n.data["extra_data"].(string); ok {
			ev, err := vars.Parse(s)
			if err != nil {
				jfe.Add("extra_data", err.Error())
			}
			n.data["extra_data"] = ev
		}
		if _, ok := jm["instance_groups"]; ok {
			jfe.Add("instance_groups", "Instance groups cannot be set on workflow nodes yet (roadmap M3-03).")
		}
		for _, f := range []string{"credentials", "labels"} {
			list, ok := jm[f].([]any)
			if !ok {
				continue
			}
			for _, x := range list {
				id, ok := api.ID(x)
				if !ok {
					jfe.Add(f, "A valid integer is required.")
					continue
				}
				if f == "credentials" {
					n.creds = append(n.creds, id)
				} else {
					n.labels = append(n.labels, id)
				}
			}
		}
		if err := jfe.Err(); err != nil {
			jobErrs[i], bad = err.(*api.Error).Body, true
			continue
		}
		typ := ujtType(c, ujt)
		plural, allowed := bulkTemplateTypes[typ]
		if typ == "" {
			jobErrs[i], bad = Obj{"unified_job_template": []string{fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", ujt)}}, true
			continue
		}
		if !allowed {
			return 0, nonFieldError(fmt.Sprintf("Template types %s not allowed in bulk jobs", strings.Title(strings.ReplaceAll(typ, "_", " "))))
		}
		if ok, err := ujtPerm(c, ujt, "execute"); err != nil {
			return 0, err
		} else if !ok {
			denied[plural] = append(denied[plural], strconv.FormatInt(ujt, 10))
			continue
		}
		if err := checkBulkNode(c, typ, ujt, n.data, n.creds, n.labels); err != nil {
			var ae *api.Error
			if !errors.As(err, &ae) {
				return 0, err
			}
			if ae.Status == 403 {
				return 0, err
			}
			jobErrs[i], bad = ae.Body, true
			continue
		}
		nodes[i] = n
	}
	if len(denied) > 0 {
		var kinds []string
		for k := range denied {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		return 0, nonFieldError(fmt.Sprintf("%s %s not found or you don't have permissions to access it", kinds[0], pyRepr("{", "}", denied[kinds[0]], false)))
	}
	if bad {
		return 0, &api.Error{Status: 400, Body: Obj{"jobs": jobErrs}}
	}

	org, err := bulkOrganization(c, body["organization"])
	if err != nil {
		return 0, err
	}
	var inv any
	if v, ok := body["inventory"]; ok && v != nil {
		id, ok := api.ID(v)
		if !ok {
			return 0, api.FieldError("inventory", "Incorrect type. Expected pk value.")
		}
		if err := api.RequirePermID(c, "inventory", "inventories", id, "use"); err != nil {
			return 0, err
		}
		inv = id
	}
	ev := map[string]any{}
	switch x := body["extra_vars"].(type) {
	case map[string]any:
		ev = x
	case string:
		if ev, err = vars.Parse(x); err != nil {
			return 0, api.FieldError("extra_vars", err.Error())
		}
	}
	if err := refuseAnsibleVars(c, "extra_vars", ev); err != nil {
		return 0, err
	}

	values := map[string]any{
		"type": "workflow_job", "name": name, "description": str(body["description"]), "organization_id": org,
		"launch_type": "manual", "extra_vars": vars.PyJSON(ev), "inventory_id": inv, "is_bulk_job": true,
		"limit": str(body["limit"]), "scm_branch": str(body["scm_branch"]), "job_tags": str(body["job_tags"]), "skip_tags": str(body["skip_tags"]),
	}
	if !c.User.Anonymous() {
		values["created_by_id"], values["modified_by_id"] = c.User.UserID, c.User.UserID
	}
	var wfID int64
	err = c.WithTx(func(c *Ctx) error {
		if wfID, err = jobs.Create(c, c.DB(), values); err != nil {
			return err
		}
		for _, n := range nodes {
			d := n.data
			idf := n.identifier
			if idf == "" {
				c.DB().QueryRow(c, "SELECT gen_random_uuid()::text").Scan(&idf)
			}
			extra := d["extra_data"]
			if extra == nil {
				extra = map[string]any{}
			}
			var nodeID int64
			err := c.DB().QueryRow(c, `INSERT INTO workflow_job_nodes (workflow_job_id, unified_job_template_id, identifier, all_parents_must_converge,
				extra_data, inventory_id, execution_environment_id, scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks,
				job_slice_count, timeout) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
				wfID, n.ujt, idf, boolv(d["all_parents_must_converge"]), extra, nullInt(d["inventory"]), nullInt(d["execution_environment"]),
				nullStr(d["scm_branch"]), nullStr(d["job_type"]), nullStr(d["job_tags"]), nullStr(d["skip_tags"]), nullStr(d["limit"]),
				d["diff_mode"], nullInt(d["verbosity"]), nullInt(d["forks"]), nullInt(d["job_slice_count"]), nullInt(d["timeout"])).Scan(&nodeID)
			if err != nil {
				return err
			}
			for _, cid := range n.creds {
				if _, err := c.DB().Exec(c, "INSERT INTO workflow_job_node_credentials (node_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", nodeID, cid); err != nil {
					return err
				}
			}
			for _, lid := range n.labels {
				if _, err := c.DB().Exec(c, "INSERT INTO workflow_job_node_labels (node_id, label_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", nodeID, lid); err != nil {
					return err
				}
			}
		}
		jobs.Wake(c, c.DB())
		return nil
	})
	return wfID, err
}

// checkBulkNode checks a bulk job's prompts as a workflow node's: the
// template must prompt for them, and the objects they name must be usable.
func checkBulkNode(c *Ctx, typ string, ujt int64, data Obj, creds, labels []int64) error {
	if d, ok := data["diff_mode"]; ok && d != nil {
		if s, ok := d.(string); ok {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return api.FieldError("diff_mode", "Must be a valid boolean.")
			}
			data["diff_mode"] = b
		} else if _, ok := d.(bool); !ok {
			return api.FieldError("diff_mode", "Must be a valid boolean.")
		}
	}
	if err := validateSchedulePrompts(c, ujt, data, data, nil); err != nil {
		return err
	}
	if len(creds) == 0 && len(labels) == 0 {
		return nil
	}
	var tpl Obj
	if typ == "job_template" {
		tpl, _ = api.Get(c, api.Lookup("job_templates"), ujt)
	}
	if len(creds) > 0 {
		if tpl == nil || !boolv(tpl["ask_credential_on_launch"]) {
			return api.FieldError("credentials", "Field is not configured to prompt on launch.")
		}
		for _, id := range creds {
			if err := api.RequirePermID(c, "credentials", "credentials", id, "use"); err != nil {
				return err
			}
		}
	}
	if len(labels) > 0 {
		if tpl == nil || !boolv(tpl["ask_labels_on_launch"]) {
			return api.FieldError("labels", "Field is not configured to prompt on launch.")
		}
		for _, id := range labels {
			if err := canUseLabel(c, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// bulkOrganization is the organization a bulk job belongs to, as AWX picks
// it: the requested one if the user is a member, else the user's only one.
func bulkOrganization(c *Ctx, requested any) (any, error) {
	id, given := api.ID(requested)
	if c.User.Superuser {
		if given {
			return id, nil
		}
		return nil, nil
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	pred := c.User.Predicate(rbac.ArgFunc(arg), "organization", "member_organization", rbac.Scope{ID: "t.id", Org: "t.id"})
	rows, err := c.DB().Query(c, "SELECT t.id FROM organizations t WHERE "+pred+" ORDER BY t.id", args...)
	if err != nil {
		return nil, err
	}
	var orgs []int64
	for rows.Next() {
		var o int64
		rows.Scan(&o)
		orgs = append(orgs, o)
	}
	rows.Close()
	if given {
		for _, o := range orgs {
			if o == id {
				return id, nil
			}
		}
		return nil, nonFieldError(fmt.Sprintf("Organization %d not found or you don't have permissions to access it", id))
	}
	switch len(orgs) {
	case 1:
		return orgs[0], nil
	case 0:
		return nil, nonFieldError("User not part of any organization, please assign an organization to assign to the bulk job")
	}
	return nil, nonFieldError("User has permission to multiple Organizations, please set one of them in the request")
}

// nullInt is a nullable integer column: NULL when the prompt is not given
// (0 is a value).
func nullInt(v any) any {
	if id, ok := api.ID(v); ok {
		return id
	}
	return nil
}

// nullStr is a nullable text column: NULL when the prompt is not given.
func nullStr(v any) any {
	if v == nil {
		return nil
	}
	return fmt.Sprint(v)
}

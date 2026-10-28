package resources

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/vars"
)

// registerRelatedLists adds AWX's sub-lists that cross resources, once all
// are registered. Each lists what the user sees of its target, under a
// parent the user sees (the parent's own access rules).
func registerRelatedLists() {
	projects := api.Lookup("projects")
	// Projects the team holds a role on (AWX's TeamProjectsList).
	api.Lookup("teams").SubLists = append(api.Lookup("teams").SubLists,
		&SubList{Name: "projects", Target: "projects", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return rbac.TeamPredicate(rbac.ArgFunc(arg), pid, "project", "view_project", projects.Scope("t"))
			}},
		// Credentials the team may use or administer, granted on the
		// credential itself; creating one here gives the team its admin role
		// (AWX's TeamCredentialsList).
		&SubList{Name: "credentials", Target: "credentials", ParentField: "team",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf(`EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
					WHERE ra.team_id = %s AND ra.content_type = 'credential' AND ra.object_id = t.id AND rd.permissions && ARRAY['use_credential', 'change_credential'])`, arg(pid))
			}},
	)
	users := api.Lookup("users")
	users.SubLists = append(users.SubLists,
		// Projects both the user and the requester can see (AWX's UserProjectsList).
		&SubList{Name: "projects", Target: "projects", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return rbac.UserPredicate(rbac.ArgFunc(arg), pid, "project", "view_project", projects.Scope("t"))
			}},
		// Who has authority over the user: superusers, the user, and the
		// admins of their organizations and of their teams (the users holding
		// roles above the user's roles, in AWX's ResourceAccessList).
		&SubList{Name: "access_list", Target: "users", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				u := arg(pid)
				return fmt.Sprintf(`t.is_superuser OR t.id = %[1]s OR EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
					WHERE (ra.user_id = t.id OR ra.team_id IN (SELECT user_teams(t.id))) AND (
						(ra.content_type IS NULL AND 'change_organization' = ANY(rd.permissions))
						OR (ra.content_type = 'organization' AND 'change_organization' = ANY(rd.permissions) AND ra.object_id IN (
							SELECT ma.object_id FROM role_assignments ma JOIN role_definitions md ON md.id = ma.role_definition_id
							WHERE (ma.user_id = %[1]s OR ma.team_id IN (SELECT user_teams(%[1]s))) AND ma.content_type = 'organization'
								AND 'member_organization' = ANY(md.permissions)))
						OR (ra.content_type = 'team' AND 'change_team' = ANY(rd.permissions) AND ra.object_id IN (SELECT user_teams(%[1]s)))))`, u)
			}},
	)
	api.Lookup("inventories").SubLists = append(api.Lookup("inventories").SubLists,
		&SubList{Name: "job_templates", Target: "job_templates", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.inventory_id = " + arg(pid) }})
	// Events of the hosts in the group (AWX's GroupJobEventsList).
	api.Lookup("groups").SubLists = append(api.Lookup("groups").SubLists,
		&SubList{Name: "job_events", Target: "job_events", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return "t.host_id IN (SELECT host_id FROM group_hosts WHERE group_id = " + arg(pid) + ")"
			}})
	// The workflow jobs that ran a sliced template's slices.
	api.Lookup("job_templates").SubLists = append(api.Lookup("job_templates").SubLists,
		&SubList{Name: "slice_workflow_jobs", Target: "workflow_jobs", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.is_sliced_job AND t.job_template_id = " + arg(pid) }})
	// The events nested under an event (a play's tasks, a task's results).
	api.Lookup("job_events").SubLists = append(api.Lookup("job_events").SubLists,
		&SubList{Name: "children", Target: "job_events", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				p := arg(pid)
				return fmt.Sprintf("t.job_id = (SELECT job_id FROM job_events WHERE id = %[1]s) AND t.parent_uuid = (SELECT uuid FROM job_events WHERE id = %[1]s)", p)
			}})
	jobsRes := api.Lookup("jobs")
	jobsRes.Actions = append(jobsRes.Actions, &api.Action{Name: "create_schedule", Methods: []string{"GET", "POST"}, Handler: handleCreateSchedule})
}

// jobLaunchPrompts are the prompts a job was launched with, as AWX's
// launch_config keeps them: the job's value of each field its template
// prompts for, and the variables it got at launch (not the template's own).
func jobLaunchPrompts(c *Ctx, job, jt Obj) (Obj, []int64) {
	prompts := Obj{}
	for field, flag := range promptable {
		if !boolv(jt[flag]) {
			continue
		}
		switch field {
		case "credentials", "labels", "instance_groups":
			continue
		}
		if v := job[field]; v != nil {
			prompts[field] = v
		}
	}
	ev, _ := vars.Parse(str(job["extra_vars"]))
	own, _ := vars.Parse(str(jt["extra_vars"]))
	extra := map[string]any{}
	for k, v := range ev {
		if prev, ok := own[k]; !ok || vars.MustJSON(prev) != vars.MustJSON(v) {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		prompts["extra_data"] = extra
	}
	var creds []int64
	if boolv(jt["ask_credential_on_launch"]) {
		jid, _ := api.ID(job["id"])
		creds = jobCredentialIDs(c, jid)
	}
	return prompts, creds
}

// handleCreateSchedule is /jobs/N/create_schedule/ (AWX's JobCreateSchedule):
// GET tells whether the job can be scheduled and with which prompts, POST
// creates a disabled monthly schedule of its template with them.
func handleCreateSchedule(c *Ctx, w http.ResponseWriter, r *http.Request, job Obj) {
	jid, _ := api.ID(job["id"])
	var jt Obj
	if id, ok := api.ID(job["job_template"]); ok {
		jt, _ = api.Get(c, api.Lookup("job_templates"), id)
	}
	if jt != nil {
		if ok, err := api.HasPerm(c, api.Lookup("job_templates"), jt, "execute"); err != nil || !ok {
			api.WriteError(w, api.ErrForbidden)
			return
		}
	}
	creds := jobCredentialIDs(c, jid)
	needsPasswords := len(passwordsNeeded(c, creds)) > 0
	var prompts Obj
	var promptCreds []int64
	if jt != nil {
		prompts, promptCreds = jobLaunchPrompts(c, job, jt)
	}
	if r.Method == "GET" {
		shown := Obj{}
		for k, v := range prompts {
			shown[k] = v
		}
		if id, ok := api.ID(shown["inventory"]); ok {
			var name, desc string
			c.DB().QueryRow(c, "SELECT name, description FROM inventories WHERE id = $1", id).Scan(&name, &desc)
			shown["inventory"] = Obj{"id": id, "name": name, "description": desc}
		}
		if len(promptCreds) > 0 {
			list := []Obj{}
			for _, id := range promptCreds {
				var name, desc string
				c.DB().QueryRow(c, "SELECT name, description FROM credentials WHERE id = $1", id).Scan(&name, &desc)
				list = append(list, Obj{"id": id, "name": name, "description": desc})
			}
			shown["credentials"] = list
		}
		api.WriteJSON(w, 200, Obj{"can_schedule": jt != nil && !needsPasswords, "prompts": shown})
		return
	}
	switch {
	case needsPasswords:
		api.WriteJSON(w, 400, Obj{"error": "Cannot create schedule because job requires credential passwords."})
		return
	case jt == nil:
		api.WriteJSON(w, 400, Obj{"error": "Cannot create schedule because a related resource is missing."})
		return
	}
	name := fmt.Sprintf("Auto-generated schedule from job %d", jid)
	taken := map[string]bool{}
	rows, err := c.DB().Query(c, "SELECT name FROM schedules WHERE name LIKE $1", name+"%")
	if err != nil {
		api.WriteError(w, err)
		return
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		taken[n] = true
	}
	rows.Close()
	for i := 1; taken[name]; i++ {
		name = fmt.Sprintf("Auto-generated schedule from job %d - number %d", jid, i)
	}
	data := Obj{"name": name, "unified_job_template": jt["id"], "enabled": false,
		"rrule": time.Now().UTC().Format("DTSTART:20060102T150405Z") + " RRULE:FREQ=MONTHLY;INTERVAL=1"}
	keys := make([]string, 0, len(prompts))
	for k := range prompts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		data[k] = prompts[k]
	}
	schedules := api.Lookup("schedules")
	var out Obj
	err = c.WithTx(func(c *Ctx) error {
		obj, err := api.Create(c, schedules, data, nil)
		if err != nil {
			return err
		}
		sid, _ := api.ID(obj["id"])
		for _, cid := range promptCreds {
			if _, err := c.DB().Exec(c, "INSERT INTO schedule_credentials (schedule_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", sid, cid); err != nil {
				return err
			}
		}
		out, err = api.SerializeOne(c, schedules, obj)
		return err
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	w.Header().Set("Location", str(out["url"]))
	api.WriteJSON(w, 201, out)
}

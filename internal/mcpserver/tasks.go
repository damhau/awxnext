package mcpserver

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/damhau/awxnext/internal/api"
)

// A unified job type's events and the events' field naming it.
var eventSets = map[string][2]string{
	"job":              {"job_events", "job"},
	"ad_hoc_command":   {"ad_hoc_command_events", "ad_hoc_command"},
	"project_update":   {"project_update_events", "project_update"},
	"inventory_update": {"inventory_update_events", "inventory_update"},
	"system_job":       {"system_job_events", "system_job"},
	"collection_sync":  {"collection_sync_events", "collection_sync"},
}

const (
	maxOutputLines     = 500
	defaultOutputLines = 100
	failureMessageLen  = 600
	failedEventsPage   = 200
)

func readTool(name, title, desc string, schema *jsonschema.Schema, run func(c *call, args map[string]any) (any, error)) *tool {
	return &tool{def: &mcp.Tool{Name: name, Description: desc, InputSchema: schema, Annotations: readOnly(title)}, run: run}
}

func writeTool(name, title, desc string, ann *mcp.ToolAnnotations, schema *jsonschema.Schema, run func(c *call, args map[string]any) (any, error)) *tool {
	ann.Title = title
	return &tool{write: true, def: &mcp.Tool{Name: name, Description: desc, InputSchema: schema, Annotations: ann}, run: run}
}

func idSchema(desc string) *jsonschema.Schema {
	return object([]string{"id"}, prop{"id", integer(desc)})
}

// unifiedJob finds any job by id: its resource's path and the job.
func (c *call) unifiedJob(id int64) (string, map[string]any, error) {
	l, _, err := c.list("unified_jobs/", url.Values{"id": {strconv.FormatInt(id, 10)}})
	if err != nil {
		return "", nil, err
	}
	if len(l) == 0 {
		return "", nil, &apiError{status: 404, body: fmt.Sprintf("No job %d that the user may see.", id)}
	}
	typ, _ := l[0]["type"].(string)
	res := api.LookupType(typ)
	if res == nil {
		return "", nil, fmt.Errorf("job %d is a %s", id, typ)
	}
	obj, err := c.getObj(fmt.Sprintf("%s/%d/", res.Name, id), nil)
	return res.Name, obj, err
}

type failedTask struct {
	Task      string   `json:"task"`
	Play      string   `json:"play"`
	Path      string   `json:"path,omitempty"`
	Message   string   `json:"message"`
	Hosts     []string `json:"hosts"`
	StartLine int64    `json:"start_line"`
	EndLine   int64    `json:"end_line"`
}

// failures are a job's failed tasks in the order they failed, with their
// hosts: the failed events of tasks whose errors aren't ignored, and the
// unreachable hosts.
func (c *call) failures(typ string, id int64) ([]*failedTask, error) {
	set, ok := eventSets[typ]
	if !ok {
		return nil, nil
	}
	events, _, err := c.list(set[0]+"/", url.Values{set[1]: {strconv.FormatInt(id, 10)}, "event__in": {"runner_on_failed,runner_on_unreachable"},
		"order_by": {"counter"}, "page_size": {strconv.Itoa(failedEventsPage)}})
	if err != nil {
		return nil, err
	}
	out := []*failedTask{}
	byTask := map[string]*failedTask{}
	for _, e := range events {
		data, _ := e["event_data"].(map[string]any)
		if e["event"] == "runner_on_failed" && fmt.Sprint(data["ignore_errors"]) == "true" {
			continue
		}
		task, _ := e["task"].(string)
		play, _ := e["play"].(string)
		key := play + "\x00" + task
		f := byTask[key]
		if f == nil {
			start, _ := api.ID(e["start_line"])
			end, _ := api.ID(e["end_line"])
			stdout, _ := e["stdout"].(string)
			f = &failedTask{Task: task, Play: play, Path: taskPath(data), Message: failureMessage(data, stdout), StartLine: start, EndLine: end, Hosts: []string{}}
			byTask[key] = f
			out = append(out, f)
		}
		if h, _ := e["host_name"].(string); h != "" {
			f.Hosts = append(f.Hosts, h)
		}
	}
	return out, nil
}

func failureMessage(data map[string]any, stdout string) string {
	res, _ := data["res"].(map[string]any)
	msg := ""
	for _, k := range []string{"msg", "stderr", "reason", "module_stderr", "stdout"} {
		if s, ok := res[k].(string); ok && strings.TrimSpace(s) != "" {
			msg = s
			break
		}
	}
	if msg == "" {
		msg, _, _ = strings.Cut(strings.ReplaceAll(ansi.ReplaceAllString(stdout, ""), "\r", ""), "\n")
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > failureMessageLen {
		msg = string(r[:failureMessageLen])
	}
	return msg
}

func taskPath(data map[string]any) string {
	path, _ := data["task_path"].(string)
	if i := strings.Index(path, "/project/"); i >= 0 {
		return path[i+len("/project/"):]
	}
	return path
}

// jobSummary keeps the fields of a job that say what it ran and how it went.
func jobSummary(j map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "type", "name", "status", "failed", "launch_type", "created", "started", "finished", "elapsed",
		"job_explanation", "result_traceback", "limit", "scm_branch", "scm_revision", "playbook", "job_type", "verbosity", "summary_fields",
		"module_name", "module_args", "extra_vars", "job_tags", "skip_tags"} {
		if v, ok := j[k]; ok && v != nil && v != "" {
			out[k] = v
		}
	}
	if sf, ok := out["summary_fields"].(map[string]any); ok {
		delete(sf, "user_capabilities")
	}
	return out
}

func jobTools() []*tool {
	templateType := enum("job_template or workflow_job_template.", "job_template", "workflow_job_template")
	launchProps := []prop{
		{"id", integer("The template's id.")},
		{"extra_vars", anyValue("Extra variables, and the survey's answers by variable: an object.")},
		{"limit", str("Hosts or groups to run on, as Ansible's --limit.")},
		{"inventory", integer("The id of the inventory, when the template asks for it at launch.")},
		{"credentials", &jsonschema.Schema{Type: "array", Items: integer("A credential's id."), Description: "The credentials, when the template asks for them at launch."}},
		{"scm_branch", str("The branch, when the template asks for it.")},
		{"job_type", enum("run, or check for a dry run, when the template asks for it.", "run", "check")},
		{"job_tags", str("Tags to run, separated by commas.")},
		{"skip_tags", str("Tags to skip, separated by commas.")},
		{"verbosity", integer("0 to 5.")},
		{"diff_mode", boolean("Show the changes made to files.")},
		{"labels", &jsonschema.Schema{Type: "array", Items: integer("A label's id.")}},
		{"execution_environment", integer("The id of the execution environment, when the template asks for it.")},
	}
	launch := func(res string) func(c *call, args map[string]any) (any, error) {
		return func(c *call, args map[string]any) (any, error) {
			id, err := idArg(args, "id")
			if err != nil {
				return nil, err
			}
			delete(args, "id")
			return c.do("POST", fmt.Sprintf("%s/%d/launch/", res, id), nil, args)
		}
	}
	return []*tool{
		readTool("get_launch_prompts", "What a launch asks", "What launching a template asks for before it runs: the fields it prompts for, their defaults, the credentials that need passwords, and its survey's questions. Call it before a launch.",
			object([]string{"template_type", "id"}, prop{"template_type", templateType}, prop{"id", integer("The template's id.")}),
			func(c *call, args map[string]any) (any, error) {
				res, id, err := templateArgs(args)
				if err != nil {
					return nil, err
				}
				prompts, err := c.getObj(fmt.Sprintf("%s/%d/launch/", res, id), nil)
				if err != nil {
					return nil, err
				}
				out := map[string]any{"launch": prompts}
				if prompts["survey_enabled"] == true {
					if out["survey"], err = c.get(fmt.Sprintf("%s/%d/survey_spec/", res, id), nil); err != nil {
						return nil, err
					}
				}
				return out, nil
			}),
		writeTool("launch_job_template", "Launch a job template", "Launch a job template, with the answers get_launch_prompts asked for. Returns the job; follow it with get_job_summary.",
			changes("", true, false), object([]string{"id"}, launchProps...), launch("job_templates")),
		writeTool("launch_workflow_job_template", "Launch a workflow", "Launch a workflow job template, with the answers get_launch_prompts asked for. Returns the workflow job.",
			changes("", true, false), object([]string{"id"}, launchProps...), launch("workflow_job_templates")),
		writeTool("launch_ad_hoc_command", "Run an ad hoc command", "Run one Ansible module on an inventory's hosts, as ansible -m does.",
			changes("", true, false), object([]string{"inventory", "module_name", "credential"},
				prop{"inventory", integer("The inventory's id.")},
				prop{"limit", str("Hosts or groups to run on; empty: all.")},
				prop{"credential", integer("The machine credential's id.")},
				prop{"module_name", str("The module, such as command, shell, ping or setup.")},
				prop{"module_args", str("The module's arguments.")},
				prop{"become_enabled", boolean("Run with privilege escalation.")},
				prop{"verbosity", integer("0 to 5.")},
				prop{"forks", integer("Hosts in parallel; 0: the default.")},
				prop{"job_type", enum("run, or check for a dry run.", "run", "check")},
				prop{"extra_vars", anyValue("Extra variables: an object.")},
				prop{"execution_environment", integer("The id of the execution environment.")},
			),
			func(c *call, args map[string]any) (any, error) { return c.do("POST", "ad_hoc_commands/", nil, args) }),
		readTool("get_job_summary", "Summarize a job", "A job of any kind: what ran, its status, and when it failed, the tasks that failed with their hosts and messages. For a workflow, its nodes' jobs.",
			idSchema("The job's id."), jobSummaryTool),
		readTool("get_job_output", "Read a job's output", fmt.Sprintf("A job's output, as plain text lines: by default the last %d, or start_line to end_line (from 0, end excluded), or the lines around its first failure. At most %d lines at a time; total_lines says how many there are.", defaultOutputLines, maxOutputLines),
			object([]string{"id"},
				prop{"id", integer("The job's id.")},
				prop{"start_line", integer("The first line, from 0.")},
				prop{"end_line", integer("The line after the last.")},
				prop{"around_failure", boolean("The lines around the first failed task.")},
				prop{"context_lines", integer("With around_failure: lines before and after it, 20 by default.")},
			), jobOutputTool),
		readTool("get_job_changes", "What changed since the last good run", "What differs between a job (or workflow job) and its template's last successful run: the project's commits, the inventory, the variables, the credentials and the execution environment.",
			idSchema("The job's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				path, _, err := c.unifiedJob(id)
				if err != nil {
					return nil, err
				}
				if path != "jobs" && path != "workflow_jobs" {
					return nil, errors.New("Only jobs and workflow jobs have changes since their last good run.")
				}
				return c.get(fmt.Sprintf("%s/%d/changes/", path, id), nil)
			}),
		writeTool("relaunch_job", "Relaunch a job", "Run a job again with the same settings: on all its hosts, or only on those that failed.",
			changes("", true, false), object([]string{"id"}, prop{"id", integer("The job's id.")}, prop{"hosts", enum("all (the default) or failed: only the hosts that failed.", "all", "failed")}),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				path, _, err := c.unifiedJob(id)
				if err != nil {
					return nil, err
				}
				body := map[string]any{}
				if h := strArg(args, "hosts"); h != "" {
					if path != "jobs" {
						return nil, errors.New("Only a playbook job relaunches on its failed hosts.")
					}
					body["hosts"] = h
				}
				return c.do("POST", fmt.Sprintf("%s/%d/relaunch/", path, id), nil, body)
			}),
		writeTool("cancel_job", "Cancel a job", "Cancel a job that is pending, waiting or running.",
			changes("", true, true), idSchema("The job's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				path, _, err := c.unifiedJob(id)
				if err != nil {
					return nil, err
				}
				if _, err := c.do("POST", fmt.Sprintf("%s/%d/cancel/", path, id), nil, map[string]any{}); err != nil {
					return nil, err
				}
				return map[string]any{"canceled": id}, nil
			}),
		approvalTool("approve"),
		approvalTool("deny"),
		writeTool("sync_project", "Sync a project", "Update a project from its source control: the jobs launched after it run the new commit.",
			changes("", false, false), idSchema("The project's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.do("POST", fmt.Sprintf("projects/%d/update/", id), nil, map[string]any{})
			}),
	}
}

func templateArgs(args map[string]any) (string, int64, error) {
	id, err := idArg(args, "id")
	if err != nil {
		return "", 0, err
	}
	switch strArg(args, "template_type") {
	case "job_template":
		return "job_templates", id, nil
	case "workflow_job_template":
		return "workflow_job_templates", id, nil
	}
	return "", 0, errors.New("template_type is job_template or workflow_job_template.")
}

func approvalTool(verb string) *tool {
	what := "Approve"
	if verb == "deny" {
		what = "Deny"
	}
	desc := what + " a pending approval: a workflow's approval node, or a self-service request's. A comment is kept with the decision."
	if verb == "deny" {
		desc += " Denying a self-service request needs one: it tells the requester why."
	}
	return writeTool(verb+"_workflow_approval", what+" an approval", desc,
		changes("", verb == "deny", false), object([]string{"id"}, prop{"id", integer("The approval's id.")}, prop{"comment", str("Why, for the requester and the record.")}),
		func(c *call, args map[string]any) (any, error) {
			id, err := idArg(args, "id")
			if err != nil {
				return nil, err
			}
			body := map[string]any{}
			if s := strArg(args, "comment"); s != "" {
				body["comment"] = s
			}
			if _, err := c.do("POST", fmt.Sprintf("workflow_approvals/%d/%s/", id, verb), nil, body); err != nil {
				return nil, err
			}
			return c.get(fmt.Sprintf("workflow_approvals/%d/", id), nil)
		})
}

func jobSummaryTool(c *call, args map[string]any) (any, error) {
	id, err := idArg(args, "id")
	if err != nil {
		return nil, err
	}
	path, job, err := c.unifiedJob(id)
	if err != nil {
		return nil, err
	}
	typ, _ := job["type"].(string)
	out := map[string]any{"job": jobSummary(job)}
	if typ == "workflow_job" {
		nodes, _, err := c.list(fmt.Sprintf("%s/%d/workflow_nodes/", path, id), url.Values{"page_size": {"200"}})
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, n := range nodes {
			sf, _ := n["summary_fields"].(map[string]any)
			list = append(list, map[string]any{"id": n["id"], "identifier": n["identifier"], "job": n["job"], "do_not_run": n["do_not_run"],
				"template": sf["unified_job_template"], "job_status": sf["job"]})
		}
		out["nodes"] = list
		return out, nil
	}
	if s, _ := job["status"].(string); s == "failed" || s == "error" {
		f, err := c.failures(typ, id)
		if err != nil {
			return nil, err
		}
		out["failed_tasks"] = f
	}
	return out, nil
}

func jobOutputTool(c *call, args map[string]any) (any, error) {
	id, err := idArg(args, "id")
	if err != nil {
		return nil, err
	}
	path, job, err := c.unifiedJob(id)
	if err != nil {
		return nil, err
	}
	if path == "workflow_jobs" || path == "workflow_approvals" {
		return nil, errors.New("A workflow has no output of its own: read its nodes' jobs (get_job_summary lists them).")
	}
	start, hasStart := api.ID(args["start_line"])
	end, hasEnd := api.ID(args["end_line"])
	if args["around_failure"] == true {
		typ, _ := job["type"].(string)
		f, err := c.failures(typ, id)
		if err != nil {
			return nil, err
		}
		if len(f) == 0 {
			return nil, errors.New("No task failed in this job.")
		}
		ctxLines := int64(20)
		if n, ok := api.ID(args["context_lines"]); ok && n >= 0 {
			ctxLines = n
		}
		start, end = max(f[0].StartLine-ctxLines, 0), f[0].EndLine+ctxLines
		hasStart, hasEnd = true, true
	}
	if !hasStart && !hasEnd {
		total, err := c.outputLines(path, id)
		if err != nil {
			return nil, err
		}
		start, end = max(total-defaultOutputLines, 0), total
	} else if !hasEnd {
		end = start + defaultOutputLines
	}
	if start < 0 || end <= start {
		return nil, errors.New("Give start_line from 0 and end_line after it.")
	}
	if end-start > maxOutputLines {
		end = start + maxOutputLines
	}
	return c.output(path, id, start, end)
}

func (c *call) outputLines(path string, id int64) (int64, error) {
	m, err := c.getObj(fmt.Sprintf("%s/%d/stdout/", path, id), url.Values{"format": {"json"}, "start_line": {"0"}, "end_line": {"1"}})
	if err != nil {
		return 0, err
	}
	r, _ := m["range"].(map[string]any)
	n, _ := api.ID(r["absolute_end"])
	return n, nil
}

func (c *call) output(path string, id, start, end int64) (any, error) {
	m, err := c.getObj(fmt.Sprintf("%s/%d/stdout/", path, id), url.Values{"format": {"json"}, "start_line": {strconv.FormatInt(start, 10)}, "end_line": {strconv.FormatInt(end, 10)}})
	if err != nil {
		return nil, err
	}
	r, _ := m["range"].(map[string]any)
	content, _ := m["content"].(string)
	return map[string]any{"start_line": r["start"], "end_line": r["end"], "total_lines": r["absolute_end"], "content": ansi.ReplaceAllString(content, "")}, nil
}

func inventoryTools() []*tool {
	return []*tool{
		readTool("get_host_details", "A host's details", "A host: its variables, its inventory, the groups it is in (directly or not), and its latest results per job.",
			idSchema("The host's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				host, err := c.getObj(fmt.Sprintf("hosts/%d/", id), nil)
				if err != nil {
					return nil, err
				}
				groups, _, err := c.list(fmt.Sprintf("hosts/%d/all_groups/", id), url.Values{"page_size": {"200"}, "order_by": {"name"}})
				if err != nil {
					return nil, err
				}
				names := []any{}
				for _, g := range groups {
					names = append(names, map[string]any{"id": g["id"], "name": g["name"]})
				}
				results, _, err := c.list(fmt.Sprintf("hosts/%d/job_host_summaries/", id), url.Values{"page_size": {"10"}, "order_by": {"-id"}})
				if err != nil {
					return nil, err
				}
				return map[string]any{"host": host, "groups": names, "recent_results": results}, nil
			}),
		readTool("get_host_facts", "A host's facts", "The Ansible facts the host's last fact-gathering job stored.",
			idSchema("The host's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("hosts/%d/ansible_facts/", id), nil)
			}),
		readTool("get_host_history", "A host's task history", "What the latest jobs did on a host, task by task: changed, failed or ok, and the tasks whose results drift from one run to the next.",
			idSchema("The host's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("hosts/%d/task_history/", id), nil)
			}),
		writeTool("sync_inventory_source", "Sync an inventory source", "Update an inventory from one of its sources: a cloud, a file in a project, or a script.",
			changes("", false, false), idSchema("The inventory source's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.do("POST", fmt.Sprintf("inventory_sources/%d/update/", id), nil, map[string]any{})
			}),
	}
}

func pick(o map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := o[k]; ok {
			out[k] = v
		}
	}
	return out
}

func jobRows(l []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(l))
	for _, j := range l {
		row := pick(j, "id", "type", "name", "status", "launch_type", "created", "started", "finished", "elapsed", "job_explanation")
		if sf, ok := j["summary_fields"].(map[string]any); ok {
			row["launched_by"] = sf["created_by"]
		}
		out = append(out, row)
	}
	return out
}

var reports = []string{"totals", "templates", "trends", "hosts", "launches"}

func monitoringTools() []*tool {
	return []*tool{
		readTool("get_system_health", "System health", "The instances that run jobs, with their capacity, state and last health check, and the instance groups, with their capacity and running jobs.",
			object(nil),
			func(c *call, args map[string]any) (any, error) {
				instances, _, err := c.list("instances/", url.Values{"page_size": {"200"}, "order_by": {"hostname"}})
				if err != nil {
					return nil, err
				}
				groups, _, err := c.list("instance_groups/", url.Values{"page_size": {"200"}, "order_by": {"name"}})
				if err != nil {
					return nil, err
				}
				is, gs := []map[string]any{}, []map[string]any{}
				for _, i := range instances {
					is = append(is, pick(i, "id", "hostname", "node_type", "node_state", "enabled", "capacity", "consumed_capacity", "percent_capacity_remaining",
						"jobs_running", "jobs_total", "last_health_check", "last_seen", "errors", "version", "cpu", "memory"))
				}
				for _, g := range groups {
					gs = append(gs, pick(g, "id", "name", "is_container_group", "capacity", "consumed_capacity", "percent_capacity_remaining", "jobs_running", "jobs_total", "instances"))
				}
				return map[string]any{"instances": is, "instance_groups": gs}, nil
			}),
		writeTool("run_health_check", "Check an instance", "Check an instance's health now: its capacity, memory, CPUs and ansible-runner.",
			changes("", false, true), idSchema("The instance's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.do("POST", fmt.Sprintf("instances/%d/health_check/", id), nil, map[string]any{})
			}),
		readTool("list_running_jobs", "Running and waiting jobs", "The jobs of every kind that are running, waiting for capacity, or pending, the latest first.",
			object(nil, prop{"page_size", integer(fmt.Sprintf("Up to %d.", maxPageSize))}),
			func(c *call, args map[string]any) (any, error) {
				q, err := listQuery(c, map[string]any{"page_size": args["page_size"], "order_by": "-id"})
				if err != nil {
					return nil, err
				}
				q.Set("status__in", "running,waiting,pending")
				l, n, err := c.list("unified_jobs/", q)
				if err != nil {
					return nil, err
				}
				return map[string]any{"count": n, "results": jobRows(l)}, nil
			}),
		readTool("list_recent_failures", "Recent failures", "The jobs of every kind that failed or errored in the last hours, the latest first. get_job_summary says why one failed.",
			object(nil, prop{"hours", integer("How far back, 24 by default.")}, prop{"name", str("Only the jobs whose name contains this.")}, prop{"page_size", integer(fmt.Sprintf("Up to %d.", maxPageSize))}),
			func(c *call, args map[string]any) (any, error) {
				hours := int64(24)
				if n, ok := api.ID(args["hours"]); ok && n > 0 {
					hours = n
				}
				q, err := listQuery(c, map[string]any{"page_size": args["page_size"], "order_by": "-finished"})
				if err != nil {
					return nil, err
				}
				q.Set("status__in", "failed,error")
				q.Set("finished__gt", time.Now().Add(-time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339))
				if s := strArg(args, "name"); s != "" {
					q.Set("name__icontains", s)
				}
				l, n, err := c.list("unified_jobs/", q)
				if err != nil {
					return nil, err
				}
				return map[string]any{"count": n, "results": jobRows(l)}, nil
			}),
		readTool("get_report", "A report", "The reports of job template runs, kept after the jobs are deleted: totals, templates (runs, failures and time per template), trends (per day), hosts (results per host) or launches (by launch type). By period (7, 30 or 90 days) or from and to.",
			object([]string{"report"},
				prop{"report", enum("Which report.", reports...)},
				prop{"period", enum("7, 30 or 90 days; 30 by default.", "7", "30", "90")},
				prop{"from", str("The first day, as 2026-11-01, instead of period.")},
				prop{"to", str("The last day.")},
				prop{"organization", integer("Only this organization's id.")},
				prop{"template", integer("Only this job template's id.")},
				prop{"inventory", integer("Only this inventory's id.")},
				prop{"project", integer("Only this project's id.")},
				prop{"label", integer("Only the templates with this label's id.")},
			),
			func(c *call, args map[string]any) (any, error) {
				report := strArg(args, "report")
				found := false
				for _, r := range reports {
					found = found || r == report
				}
				if !found {
					return nil, fmt.Errorf("report is one of %s.", strings.Join(reports, ", "))
				}
				q := url.Values{}
				for _, k := range []string{"period", "from", "to", "organization", "template", "inventory", "project", "label"} {
					if v, ok := args[k]; ok && v != nil && v != "" {
						q.Set(k, queryValue(v))
					}
				}
				return c.get("reports/"+report+"/", q)
			}),
		readTool("list_failing_notifications", "Failing notifications", "The notification templates whose latest sends failed several times in a row, with the last error and since when.",
			object(nil),
			func(c *call, args map[string]any) (any, error) {
				out := []map[string]any{}
				for page := 1; ; page++ {
					l, n, err := c.list("notification_templates/", url.Values{"page_size": {"200"}, "page": {strconv.Itoa(page)}, "order_by": {"id"}})
					if err != nil {
						return nil, err
					}
					for _, nt := range l {
						sf, _ := nt["summary_fields"].(map[string]any)
						if f, ok := sf["failing"].(map[string]any); ok {
							out = append(out, map[string]any{"id": nt["id"], "name": nt["name"], "notification_type": nt["notification_type"], "failing": f})
						}
					}
					if page*200 >= n {
						break
					}
				}
				return map[string]any{"count": len(out), "results": out}, nil
			}),
	}
}

func userTools() []*tool {
	assignment := func(args map[string]any) (string, map[string]any, error) {
		rd, err := idArg(args, "role_definition")
		if err != nil {
			return "", nil, err
		}
		body := map[string]any{"role_definition": rd}
		if v, ok := args["object_id"]; ok && v != nil {
			id, err := idArg(args, "object_id")
			if err != nil {
				return "", nil, err
			}
			body["object_id"] = id
		}
		user, hasUser := api.ID(args["user"])
		team, hasTeam := api.ID(args["team"])
		switch {
		case hasUser && !hasTeam:
			body["user"] = user
			return "role_user_assignments", body, nil
		case hasTeam && !hasUser:
			body["team"] = team
			return "role_team_assignments", body, nil
		}
		return "", nil, errors.New("Give a user or a team, not both.")
	}
	roleProps := []prop{
		{"role_definition", integer("The role definition's id: list_role_definitions lists them, such as JobTemplate Execute.")},
		{"user", integer("The user's id.")},
		{"team", integer("The team's id.")},
		{"object_id", integer("The id of the object the role is on, of the role definition's content type; none for a system role.")},
	}
	return []*tool{
		readTool("list_user_roles", "A user's roles", "The roles a user holds: their own assignments, their teams, and the roles their teams hold.",
			idSchema("The user's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				own, _, err := c.list("role_user_assignments/", url.Values{"user": {strconv.FormatInt(id, 10)}, "page_size": {"200"}})
				if err != nil {
					return nil, err
				}
				teams, _, err := c.list(fmt.Sprintf("users/%d/teams/", id), url.Values{"page_size": {"200"}})
				if err != nil {
					return nil, err
				}
				ts := []map[string]any{}
				for _, t := range teams {
					held, _, err := c.list("role_team_assignments/", url.Values{"team": {fmt.Sprint(t["id"])}, "page_size": {"200"}})
					if err != nil {
						return nil, err
					}
					ts = append(ts, map[string]any{"id": t["id"], "name": t["name"], "roles": held})
				}
				return map[string]any{"assignments": own, "teams": ts}, nil
			}),
		writeTool("grant_role", "Grant a role", "Give a user or a team a role on an object, or a system role.",
			changes("", false, true), object([]string{"role_definition"}, roleProps...),
			func(c *call, args map[string]any) (any, error) {
				path, body, err := assignment(args)
				if err != nil {
					return nil, err
				}
				return c.do("POST", path+"/", nil, body)
			}),
		writeTool("revoke_role", "Revoke a role", "Take a role on an object, or a system role, from a user or a team.",
			changes("", true, true), object([]string{"role_definition"}, roleProps...),
			func(c *call, args map[string]any) (any, error) {
				path, body, err := assignment(args)
				if err != nil {
					return nil, err
				}
				q := url.Values{"page_size": {"2"}}
				for k, v := range body {
					q.Set(k, queryValue(v))
				}
				if _, ok := body["object_id"]; !ok {
					q.Set("object_id__isnull", "true")
				}
				l, _, err := c.list(path+"/", q)
				if err != nil {
					return nil, err
				}
				if len(l) == 0 {
					return nil, &apiError{status: 404, body: "No such role assignment."}
				}
				id, _ := api.ID(l[0]["id"])
				if _, err := c.do("DELETE", fmt.Sprintf("%s/%d/", path, id), nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"revoked": id}, nil
			}),
	}
}

func securityTools() []*tool {
	return []*tool{
		writeTool("test_credential", "Test a credential", "Check that a credential's secret store answers and holds the secret: for credentials of external secret store types. It changes nothing, but needs a token that may write.",
			&mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}, object([]string{"id"}, prop{"id", integer("The credential's id.")}, prop{"metadata", anyValue("The lookup's inputs, such as the secret's path: an object.")}),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				body := map[string]any{"metadata": map[string]any{}}
				if m, ok := args["metadata"].(map[string]any); ok {
					body["metadata"] = m
				}
				return c.do("POST", fmt.Sprintf("credentials/%d/test/", id), nil, body)
			}),
		writeTool("backtest_launch_policy", "Backtest a launch policy", "Which of the past launches a launch policy, or a draft condition, would have refused. It changes nothing, but needs a token that may write.",
			&mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}, object(nil,
				prop{"id", integer("The policy's id; none to try a draft condition.")},
				prop{"condition", str("A draft condition, in CEL over launch and now.")},
				prop{"organization", integer("For a draft: the organization whose launches it covers.")},
				prop{"days", integer("How many days back, 7 by default, up to 365.")},
			),
			func(c *call, args map[string]any) (any, error) {
				body := map[string]any{}
				for _, k := range []string{"condition", "organization", "days"} {
					if v, ok := args[k]; ok && v != nil && v != "" {
						body[k] = v
					}
				}
				if _, ok := args["id"]; ok {
					id, err := idArg(args, "id")
					if err != nil {
						return nil, err
					}
					return c.do("POST", fmt.Sprintf("launch_policies/%d/backtest/", id), nil, body)
				}
				return c.do("POST", "launch_policies/backtest/", nil, body)
			}),
	}
}

var settingsCategory = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func configurationTools() []*tool {
	category := func(args map[string]any) (string, error) {
		cat := strArg(args, "category")
		if !settingsCategory.MatchString(cat) {
			return "", errors.New("category is a settings category, such as system, jobs or authentication: get_settings lists them.")
		}
		return cat, nil
	}
	return []*tool{
		readTool("get_settings", "Settings", "Without a category, the settings' categories; with one, its settings and their values. Secrets come back $encrypted$.",
			object(nil, prop{"category", str("A category's slug, such as system, jobs or authentication.")}),
			func(c *call, args map[string]any) (any, error) {
				if strArg(args, "category") == "" {
					return c.get("settings/", nil)
				}
				cat, err := category(args)
				if err != nil {
					return nil, err
				}
				return c.get("settings/"+cat+"/", nil)
			}),
		writeTool("update_settings", "Change settings", "Change settings of a category; the others keep their values. Superusers only.",
			changes("", true, true), object([]string{"category", "values"}, prop{"category", str("The category's slug.")}, prop{"values", anyValue("The settings to change and their new values: an object.")}),
			func(c *call, args map[string]any) (any, error) {
				cat, err := category(args)
				if err != nil {
					return nil, err
				}
				values, ok := args["values"].(map[string]any)
				if !ok || len(values) == 0 {
					return nil, errors.New("values is an object of the settings to change.")
				}
				return c.do("PATCH", "settings/"+cat+"/", nil, values)
			}),
		readTool("get_execution_environment_contents", "An image's contents", "What an execution environment's image holds, from its last inspection: Ansible's version, its collections and Python packages.",
			idSchema("The execution environment's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("execution_environments/%d/contents/", id), nil)
			}),
	}
}

func contentTools() []*tool {
	return []*tool{
		readTool("get_collection_details", "A collection's details", "A collection of the hub, by id or by namespace and name: its versions, the latest first, with whether each is signed and the repositories it is in.",
			object(nil, prop{"id", integer("The collection's id.")}, prop{"namespace", str("Its namespace, such as community.")}, prop{"name", str("Its name, such as general.")}),
			func(c *call, args map[string]any) (any, error) {
				var col map[string]any
				if _, ok := args["id"]; ok {
					id, err := idArg(args, "id")
					if err != nil {
						return nil, err
					}
					if col, err = c.getObj(fmt.Sprintf("collections/%d/", id), nil); err != nil {
						return nil, err
					}
				} else {
					ns, name := strArg(args, "namespace"), strArg(args, "name")
					if ns == "" || name == "" {
						return nil, errors.New("Give the collection's id, or its namespace and name.")
					}
					l, _, err := c.list("collections/", url.Values{"namespace__name": {ns}, "name": {name}})
					if err != nil {
						return nil, err
					}
					if len(l) == 0 {
						return nil, &apiError{status: 404, body: fmt.Sprintf("No collection %s.%s that the user may see.", ns, name)}
					}
					col = l[0]
				}
				versions, n, err := c.list("collection_versions/", url.Values{"collection": {fmt.Sprint(col["id"])}, "order_by": {"-id"}, "page_size": {"50"}})
				if err != nil {
					return nil, err
				}
				return map[string]any{"collection": col, "version_count": n, "versions": versions}, nil
			}),
	}
}

func eventTools() []*tool {
	return []*tool{
		writeTool("backtest_event_rule", "Backtest an event rule", "What an event rule would have decided on its stream's past events: the ones it matches and the launches it would make. It changes nothing, but needs a token that may write.",
			&mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}, object([]string{"id"},
				prop{"id", integer("The rule's id.")},
				prop{"since", str("From when, as 2026-11-15T08:00:00Z; a week ago by default.")},
				prop{"until", str("Until when; now by default.")},
				prop{"max_events", integer("The most events to replay.")},
			),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				body := map[string]any{}
				for _, k := range []string{"since", "until", "max_events"} {
					if v, ok := args[k]; ok && v != nil && v != "" {
						body[k] = v
					}
				}
				return c.do("POST", fmt.Sprintf("event_rules/%d/backtest/", id), nil, body)
			}),
		writeTool("set_event_rule_mode", "Set an event rule's mode", "What an event rule does when it matches: shadow records what it would launch, approval waits for someone to approve each launch, auto launches.",
			changes("", true, true), object([]string{"id", "mode"}, prop{"id", integer("The rule's id.")}, prop{"mode", enum("The mode.", "shadow", "approval", "auto")}),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.do("PATCH", fmt.Sprintf("event_rules/%d/", id), nil, map[string]any{"mode": strArg(args, "mode")})
			}),
	}
}

func selfServiceTools() []*tool {
	return []*tool{
		readTool("get_catalog_item_form", "A catalog item's form", "What requesting a catalog item asks: its questions with their choices and defaults, the teams to request for, and the quotas.",
			idSchema("The catalog item's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("catalog_items/%d/request/", id), nil)
			}),
		writeTool("request_catalog_item", "Request a catalog item", "Request a catalog item with the answers its form asks for. It runs at once, or waits for its approvers.",
			changes("", true, false), object([]string{"id"},
				prop{"id", integer("The catalog item's id.")},
				prop{"answers", anyValue("The answers by variable: an object.")},
				prop{"team", integer("The team to request for, when the item asks.")},
				prop{"reason", str("Why, for the approvers.")},
				prop{"lifetime_days", integer("How many days the deployment lasts, when the item lets the requester choose.")},
			),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				delete(args, "id")
				return c.do("POST", fmt.Sprintf("catalog_items/%d/request/", id), nil, args)
			}),
		readTool("get_request_progress", "A request's progress", "Where a catalog request is: waiting for approval, running (its steps), done or failed.",
			idSchema("The catalog request's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("catalog_requests/%d/progress/", id), nil)
			}),
		readTool("list_deployment_actions", "A deployment's actions", "The day-2 actions of a deployment, such as resize or restart, and what each asks.",
			idSchema("The deployment's id."),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				return c.get(fmt.Sprintf("deployments/%d/actions/", id), nil)
			}),
		writeTool("run_deployment_action", "Run a deployment action", "Run one of a deployment's day-2 actions with its answers.",
			changes("", true, false), object([]string{"id", "action"},
				prop{"id", integer("The deployment's id.")},
				prop{"action", str("The action's name, from list_deployment_actions.")},
				prop{"answers", anyValue("The answers by variable: an object.")},
				prop{"reason", str("Why, for the approvers.")},
			),
			func(c *call, args map[string]any) (any, error) {
				id, err := idArg(args, "id")
				if err != nil {
					return nil, err
				}
				delete(args, "id")
				return c.do("POST", fmt.Sprintf("deployments/%d/actions/", id), nil, args)
			}),
		readTool("list_pending_approvals", "Requests to approve", "The catalog requests waiting for the user's approval, with who asked, for what and why. approve_workflow_approval and deny_workflow_approval decide them.",
			object(nil),
			func(c *call, args map[string]any) (any, error) { return c.get("portal/approvals/", nil) }),
		approvalTool("approve"),
		approvalTool("deny"),
	}
}

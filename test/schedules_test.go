package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

func TestScheduleValidationAndFields(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true, "survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"variable": "secret", "question_name": "Secret", "type": "password", "required": false},
		map[string]any{"variable": "who", "question_name": "Who", "type": "text", "required": false},
	}}), 200)
	base := fmt.Sprintf("job_templates/%d/schedules/", jt)

	must(t, a.post(base, map[string]any{"name": "bad", "rrule": "RRULE:FREQ=DAILY"}), 400)
	must(t, a.post(base, map[string]any{"name": "bad", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=SECONDLY"}), 400)
	must(t, a.post(base, map[string]any{"name": "bad", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY", "job_tags": "x"}), 400)
	must(t, a.post(base, map[string]any{"name": "bad", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY", "extra_data": map[string]any{"other": 1}}), 400)

	r := must(t, a.post(base, map[string]any{"name": "nightly", "rrule": "DTSTART;TZID=Europe/Zurich:20300101T020000 RRULE:FREQ=DAILY;COUNT=3",
		"limit": "h1", "extra_data": map[string]any{"secret": "s3cr3t", "who": "sched"}}), 201)
	b := r.Body
	if b["timezone"] != "Europe/Zurich" || b["dtstart"] != "2030-01-01T01:00:00Z" || b["dtend"] != "2030-01-03T01:00:00Z" || b["next_run"] != "2030-01-01T01:00:00Z" {
		t.Fatalf("computed fields: %v", b)
	}
	if extra := b["extra_data"].(map[string]any); extra["secret"] != "$encrypted$" || extra["who"] != "sched" {
		t.Fatalf("extra_data: %v", extra)
	}
	if strings.Contains(r.Raw, "s3cr3t") {
		t.Fatal("survey password leaked")
	}
	sf := b["summary_fields"].(map[string]any)["unified_job_template"].(map[string]any)
	if sf["unified_job_type"] != "job" {
		t.Fatalf("summary: %v", sf)
	}
	if jtObj := a.get(fmt.Sprintf("job_templates/%d/", jt)); jtObj.Body["next_job_run"] != "2030-01-01T01:00:00Z" {
		t.Fatalf("next_job_run: %v", jtObj.Body["next_job_run"])
	}
	// "$encrypted$" keeps the stored password; disabling clears next_run.
	r = must(t, a.patch(fmt.Sprintf("schedules/%d/", r.id()), map[string]any{"enabled": false, "extra_data": map[string]any{"secret": "$encrypted$", "who": "x"}}), 200)
	if r.Body["next_run"] != nil {
		t.Fatalf("disabled next_run: %v", r.Body["next_run"])
	}
	var stored string
	app.DB.QueryRow(context.Background(), "SELECT extra_data->>'secret' FROM schedules WHERE id = $1", r.id()).Scan(&stored)
	if !strings.HasPrefix(stored, "$encrypted$v1$") {
		t.Fatalf("stored secret: %q", stored)
	}

	p := must(t, a.post("schedules/preview/", map[string]any{"rrule": "DTSTART;TZID=America/New_York:20200101T090000 RRULE:FREQ=WEEKLY;BYDAY=MO"}), 200)
	if len(p.Body["utc"].([]any)) != 10 || !strings.HasSuffix(p.Body["local"].([]any)[0].(string), "09:00:00-05:00") && !strings.HasSuffix(p.Body["local"].([]any)[0].(string), "09:00:00-04:00") {
		t.Fatalf("preview: %v", p.Body)
	}
	must(t, a.post("schedules/preview/", map[string]any{"rrule": "nope"}), 400)
	if z := a.get("schedules/zoneinfo/"); !strings.Contains(z.Raw, `"America/New_York"`) {
		t.Fatalf("zoneinfo: %.200s", z.Raw)
	}
}

func TestScheduleRunsJob(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true, "ask_variables_on_launch": true})
	s := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": "every minute",
		"rrule": "DTSTART:20200101T000000Z RRULE:FREQ=MINUTELY;INTERVAL=1", "limit": "h2", "extra_data": map[string]any{"who": "cron"}}), 201)
	next, _ := time.Parse(time.RFC3339, s.Body["next_run"].(string))
	if !next.After(time.Now()) || next.After(time.Now().Add(61*time.Second)) {
		t.Fatalf("next_run: %v", next)
	}
	app.DB.Exec(context.Background(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", s.id())
	resources.ScheduleTick(context.Background(), jobManager)
	jobsList := a.get(fmt.Sprintf("schedules/%d/jobs/", s.id()))
	if jobsList.count() != 1 {
		t.Fatalf("scheduled jobs: %v", jobsList.Body)
	}
	jid := int64(jobsList.results()[0]["id"].(float64))
	job := waitJob(t, a, fmt.Sprintf("jobs/%d/", jid))
	mustStatus(t, f, job, "successful")
	lb := job.Body["launched_by"].(map[string]any)
	if job.Body["launch_type"] != "scheduled" || lb["type"] != "schedule" || job.Body["limit"] != "h2" {
		t.Fatalf("scheduled job: %v", job.Body)
	}
	if out := f.stdout(t, jid); !strings.Contains(out, "hello cron from h2") || strings.Contains(out, "from h1") {
		t.Fatalf("stdout:\n%s", out)
	}
	if r := a.get(fmt.Sprintf("schedules/%d/", s.id())); r.Body["next_run"] == nil {
		t.Fatal("next_run not advanced")
	}
	// A second tick does not relaunch before next_run.
	resources.ScheduleTick(context.Background(), jobManager)
	if a.get(fmt.Sprintf("schedules/%d/jobs/", s.id())).count() != 1 {
		t.Fatal("schedule fired twice")
	}

	// Project and inventory source schedules.
	ps := must(t, a.post(fmt.Sprintf("projects/%d/schedules/", f.project), map[string]any{"name": "sync", "rrule": "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY"}), 201)
	must(t, a.post(fmt.Sprintf("projects/%d/schedules/", f.project), map[string]any{"name": "sync2", "rrule": "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY", "limit": "x"}), 400)
	app.DB.Exec(context.Background(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", ps.id())
	resources.ScheduleTick(context.Background(), jobManager)
	pu := a.get(fmt.Sprintf("projects/%d/project_updates/?launch_type=scheduled", f.project))
	if pu.count() != 1 {
		t.Fatalf("scheduled project updates: %v", pu.Body)
	}
	waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(pu.results()[0]["id"].(float64))))

	// RBAC: execute is needed to schedule; schedule owners may edit their own.
	user, uid := newUser(t, a)
	must(t, user.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": "u", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY"}), 403)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "job_templates", jt, "execute_role")}), 204)
	us := must(t, user.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": "u", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY"}), 201)
	must(t, user.patch(fmt.Sprintf("schedules/%d/", us.id()), map[string]any{"description": "mine"}), 200)
	must(t, user.patch(fmt.Sprintf("schedules/%d/", s.id()), map[string]any{"description": "not mine"}), 403)
	must(t, user.delete(fmt.Sprintf("schedules/%d/", us.id())), 204)
}

func TestSystemJobs(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	sjts := must(t, a.get("system_job_templates/"), 200)
	if sjts.count() != 5 {
		t.Fatalf("system job templates: %d", sjts.count())
	}
	byType := map[string]int64{}
	for _, s := range sjts.results() {
		byType[s["job_type"].(string)] = int64(s["id"].(float64))
	}
	cleanup := byType["cleanup_jobs"]
	if r := a.get(fmt.Sprintf("system_job_templates/%d/schedules/", cleanup)); r.count() != 1 || r.results()[0]["extra_data"].(map[string]any)["days"] == nil {
		// The default is days=90; the importer test may have replaced it with an imported schedule.
		t.Fatalf("default schedule: %v", r.Body)
	}
	user, _ := newUser(t, a)
	must(t, user.get(fmt.Sprintf("system_job_templates/%d/", cleanup)), 403)
	must(t, user.post(fmt.Sprintf("system_job_templates/%d/launch/", cleanup), nil), 403)
	must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", cleanup), map[string]any{"extra_vars": map[string]any{"days": -1}}), 400)
	must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", cleanup), map[string]any{"extra_vars": map[string]any{"rm": "-rf"}}), 400)

	// An old job is removed by cleanup_jobs; recent ones stay.
	jt := f.template(t, "site.yml", nil)
	old := f.launch(t, a, jt, nil)
	recent := f.launch(t, a, jt, nil)
	app.DB.Exec(context.Background(), "UPDATE unified_jobs SET created = now() - interval '10 days' WHERE id = $1", old.id())

	dry := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", cleanup), map[string]any{"extra_vars": map[string]any{"days": 5, "dry_run": true}}), 201)
	dj := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", dry.id()))
	if dj.Body["status"] != "successful" {
		t.Fatalf("dry run: %v", dj.Body)
	}
	if out := a.get(fmt.Sprintf("system_jobs/%d/stdout/?format=txt", dry.id())).Raw; !strings.Contains(out, " would delete") {
		t.Fatalf("dry run output:\n%s", out)
	}
	must(t, a.get(fmt.Sprintf("jobs/%d/", old.id())), 200)
	run := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", cleanup), map[string]any{"extra_vars": "days: 5"}), 201)
	if rj := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", run.id())); rj.Body["status"] != "successful" || rj.Body["job_type"] != "cleanup_jobs" {
		t.Fatalf("cleanup: %v", rj.Body)
	}
	must(t, a.get(fmt.Sprintf("jobs/%d/", old.id())), 404)
	must(t, a.get(fmt.Sprintf("jobs/%d/", recent.id())), 200)
	if r := a.get(fmt.Sprintf("system_job_templates/%d/jobs/", cleanup)); r.count() < 2 {
		t.Fatalf("template jobs: %v", r.Body)
	}
	if r := a.get(fmt.Sprintf("system_jobs/%d/events/", run.id())); r.count() == 0 {
		t.Fatal("no system job events")
	}

	sess := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", byType["cleanup_sessions"]), nil), 201)
	if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", sess.id())); r.Body["status"] != "successful" {
		t.Fatalf("cleanup sessions: %v", r.Body)
	}
}

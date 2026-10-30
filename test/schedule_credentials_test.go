package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/damhau/awxnext/internal/resources"
)

// Credentials saved as a schedule's prompt (roadmap M7-06, the schedule
// form and the workflow editor share the prompt lists): saved and listed,
// refused when they ask for passwords at launch, and given to the job the
// schedule launches in place of the template's of the same type.
func TestScheduleCredentials(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	machine := credType(t, a, "ssh")
	tplCred := must(t, a.post("credentials/", map[string]any{"name": uniq("tpl"), "organization": f.org, "credential_type": machine, "inputs": map[string]any{"username": "a"}}), 201).id()
	saved := must(t, a.post("credentials/", map[string]any{"name": uniq("sched"), "organization": f.org, "credential_type": machine, "inputs": map[string]any{"username": "b"}}), 201).id()
	asks := must(t, a.post("credentials/", map[string]any{"name": uniq("ask"), "organization": f.org, "credential_type": machine,
		"inputs": map[string]any{"username": "c", "password": "ASK"}}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"ask_credential_on_launch": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": tplCred}), 204)
	s := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"), "rrule": "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY;INTERVAL=1"}), 201).id()
	sub := fmt.Sprintf("schedules/%d/credentials/", s)

	must(t, a.post(sub, map[string]any{"id": asks}), 400)
	must(t, a.post(sub, map[string]any{"id": saved}), 204)
	if l := must(t, a.get(sub), 200).results(); len(l) != 1 || l[0]["id"] != float64(saved) {
		t.Fatalf("saved credentials: %v", l)
	}

	app.DB.Exec(context.Background(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", s)
	resources.ScheduleTick(context.Background(), jobManager)
	jobs := must(t, a.get(fmt.Sprintf("schedules/%d/jobs/", s)), 200).results()
	if len(jobs) != 1 {
		t.Fatalf("scheduled jobs: %v", jobs)
	}
	creds := must(t, a.get(fmt.Sprintf("jobs/%v/credentials/", jobs[0]["id"])), 200).results()
	if len(creds) != 1 || creds[0]["id"] != float64(saved) {
		t.Fatalf("the scheduled job's credentials: %v", creds)
	}
}

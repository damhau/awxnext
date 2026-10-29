package test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func gitTag(t *testing.T, dir, tag string) {
	t.Helper()
	cmd := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=t@example.com", "tag", "-a", "-m", tag, tag)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}
}

func TestSurveyDynamicChoicesAndConditions(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	gitTag(t, f.repo, "v1.0")
	commitFiles(t, f.repo, map[string]string{"NOTES": "1.1\n"}, "1.1")
	gitTag(t, f.repo, "v1.1")
	up := must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	if st := waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64)))).Body["status"]; st != "successful" {
		t.Fatalf("project update: %v", st)
	}
	jt := f.template(t, "site.yml", map[string]any{"survey_enabled": true, "ask_inventory_on_launch": true})
	q := func(m map[string]any) map[string]any {
		out := map[string]any{"required": true}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	survey := func(qs ...map[string]any) map[string]any {
		list := make([]any, len(qs))
		for i, x := range qs {
			list[i] = x
		}
		return map[string]any{"name": "s", "description": "", "spec": list}
	}
	target := q(map[string]any{"question_name": "Target", "variable": "target", "type": "multiplechoice", "choices_from": map[string]any{"type": "hosts"}})
	version := q(map[string]any{"question_name": "Version", "variable": "version", "type": "multiplechoice", "choices_from": map[string]any{"type": "git_refs", "refs": "tags"}})
	env := q(map[string]any{"question_name": "Env", "variable": "env", "type": "multiplechoice", "choices": []any{"dev", "prod"}})
	ticket := q(map[string]any{"question_name": "Ticket", "variable": "ticket", "type": "text",
		"show_if": map[string]any{"variable": "env", "values": []any{"prod"}}, "pattern": "CHG-[0-9]+", "pattern_hint": "use a change number like CHG-123"})
	invs := q(map[string]any{"question_name": "Copy to", "variable": "copy_to", "type": "multiselect", "required": false,
		"choices_from": map[string]any{"type": "resource", "resource": "inventories", "field": "name", "filter": map[string]any{"organization": f.org}}})

	// Invalid specs.
	specURL := fmt.Sprintf("job_templates/%d/survey_spec/", jt)
	for name, bad := range map[string]map[string]any{
		"choices_from on text":    survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "text", "choices_from": map[string]any{"type": "hosts"}})),
		"unknown source":          survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "multiplechoice", "choices_from": map[string]any{"type": "dns"}})),
		"unknown resource":        survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "multiplechoice", "choices_from": map[string]any{"type": "resource", "resource": "nope"}})),
		"write-only field":        survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "multiplechoice", "choices_from": map[string]any{"type": "resource", "resource": "users", "field": "password"}})),
		"bad filter":              survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "multiplechoice", "choices_from": map[string]any{"type": "resource", "resource": "hosts", "filter": map[string]any{"nope__exact": 1}}})),
		"show_if on later":        survey(ticket, env),
		"show_if without values":  survey(env, q(map[string]any{"question_name": "x", "variable": "x", "type": "text", "show_if": map[string]any{"variable": "env", "values": []any{}}})),
		"bad pattern":             survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "text", "pattern": "("})),
		"default against pattern": survey(q(map[string]any{"question_name": "x", "variable": "x", "type": "text", "pattern": "[a-z]+", "default": "ABC"})),
	} {
		if r := a.post(specURL, bad); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	must(t, a.post(specURL, survey(target, version, env, ticket, invs)), 200)

	// Choices for the launching user.
	choices := func(variable, extra string) []any {
		t.Helper()
		r := must(t, a.get(fmt.Sprintf("job_templates/%d/survey_choices/?variable=%s%s", jt, variable, extra)), 200)
		if r.Body["error"] != nil {
			t.Fatalf("choices for %s: %v", variable, r.Body["error"])
		}
		return r.Body["choices"].([]any)
	}
	if c := choices("target", ""); fmt.Sprint(c) != "[h1 h2 h3]" {
		t.Fatalf("host choices: %v", c)
	}
	if c := choices("version", ""); fmt.Sprint(c) != "[v1.1 v1.0]" {
		t.Fatalf("tag choices: %v", c)
	}
	other := must(t, a.post("inventories/", map[string]any{"name": uniq("other"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("inventories/%d/hosts/", other), map[string]any{"name": "o1"}), 201)
	if c := choices("target", fmt.Sprintf("&inventory=%d", other)); fmt.Sprint(c) != "[o1]" {
		t.Fatalf("host choices of the launch inventory: %v", c)
	}
	if c := choices("copy_to", ""); len(c) != 2 {
		t.Fatalf("inventory choices: %v", c)
	}
	must(t, a.get(fmt.Sprintf("job_templates/%d/survey_choices/?variable=nope", jt)), 404)
	user, _ := newUser(t, a)
	must(t, user.get(fmt.Sprintf("job_templates/%d/survey_choices/?variable=target", jt)), 403)

	// Launch checks answers against the same lists.
	launch := func(vars map[string]any) resp {
		t.Helper()
		return a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": vars})
	}
	needed := func(r resp) string {
		t.Helper()
		if r.Status != 400 {
			t.Fatalf("launch should fail: %d %s", r.Status, r.Raw)
		}
		return fmt.Sprint(r.Body["variables_needed_to_start"])
	}
	if msg := needed(launch(map[string]any{"target": "nope", "version": "v1.0", "env": "dev"})); !strings.Contains(msg, "Value nope for 'target' expected to be one of [h1 h2 h3]") {
		t.Fatalf("unknown host: %s", msg)
	}
	if msg := needed(launch(map[string]any{"target": "h1", "version": "v9", "env": "dev"})); !strings.Contains(msg, "'version'") {
		t.Fatalf("unknown tag: %s", msg)
	}
	if msg := needed(launch(map[string]any{"target": "h1", "version": "v1.0", "env": "prod"})); !strings.Contains(msg, "'ticket' value missing") {
		t.Fatalf("shown question required: %s", msg)
	}
	if msg := needed(launch(map[string]any{"target": "h1", "version": "v1.0", "env": "prod", "ticket": "123"})); !strings.Contains(msg, "'ticket': use a change number like CHG-123") {
		t.Fatalf("pattern: %s", msg)
	}
	ok := must(t, launch(map[string]any{"target": "h1", "version": "v1.0", "env": "dev", "ticket": "ignored"}), 201)
	if ev := must(t, a.get(fmt.Sprintf("jobs/%d/", ok.id())), 200).Body["extra_vars"].(string); strings.Contains(ev, "ticket") || !strings.Contains(ev, "v1.0") {
		t.Fatalf("hidden question's answer should be dropped: %s", ev)
	}
	ok = must(t, launch(map[string]any{"target": "h2", "version": "v1.1", "env": "prod", "ticket": "CHG-42"}), 201)
	if ev := must(t, a.get(fmt.Sprintf("jobs/%d/", ok.id())), 200).Body["extra_vars"].(string); !strings.Contains(ev, "CHG-42") {
		t.Fatalf("shown question's answer: %s", ev)
	}
	waitJob(t, a, fmt.Sprintf("jobs/%d/", ok.id()))

	// Deleting the survey answers 200 with no body, as AWX does (the collection checks it).
	if r := must(t, a.delete(specURL), 200); r.Raw != "" {
		t.Fatalf("delete body: %q", r.Raw)
	}
	if r := a.get(specURL); fmt.Sprint(r.Body) != "map[]" {
		t.Fatalf("survey after delete: %s", r.Raw)
	}
}

package launchpolicy

import (
	"context"
	"strings"
	"testing"
	"time"
)

func sample() map[string]any {
	return map[string]any{
		"type":     "job",
		"template": map[string]any{"id": int64(4), "name": "deploy", "type": "job_template", "organization": "Ops", "labels": []any{"web"}, "branch": ""},
		"labels":   []any{"web"},
		"project":  map[string]any{"id": int64(2), "name": "site", "url": "https://git.example.com/site.git", "branch": "main", "revision": "abc"},
		"playbook": "site.yml",
		"inventory": map[string]any{"id": int64(3), "name": "prod", "organization": "Ops", "labels": []any{},
			"hosts": int64(250)},
		"limit": "", "job_type": "run", "verbosity": int64(0), "diff_mode": false, "forks": int64(0),
		"image":           "quay.io/ansible/awx-ee:24.6.1",
		"credentials":     []any{map[string]any{"name": "ssh", "type": "Machine", "kind": "ssh"}},
		"instance_groups": []any{},
		"workflow":        map[string]any{"id": int64(0), "name": ""},
		"extra_vars":      map[string]any{"version": "1.2", "password": "$encrypted$"},
		"branch":          "",
		"user":            map[string]any{"id": int64(7), "username": "ana", "teams": []any{"oncall"}, "superuser": false},
		"launch_type":     "manual", "schedule": "", "webhook": "", "event_rule": "",
		"request": map[string]any{"id": int64(0), "item": ""},
		"module":  map[string]any{"name": "", "args": ""},
	}
}

func TestCheck(t *testing.T) {
	for expr, want := range map[string]string{
		`launch.inventory.name == "prod" && launch.limit == ""`: "",
		`launch.inventory.hosts > 100 && launch.limit == ""`:    "",
		`!launch.image.startsWith("registry.example.com/")`:     "",
		`launch.extra_vars.version == "1.2"`:                    "",
		`"oncall" in launch.user.teams`:                         "",
		`launch.credentials.exists(c, c.kind == "cloud")`:       "",
		`!window(now, "Mon-Thu 20:00-06:00", "Europe/Zurich")`:  "",
		`window(now, "Sat,Sun")`:                                "",
		`launch.inventroy.name == "prod"`:                       "undefined field 'inventroy'",
		`launch.inventory.hosts == "many"`:                      "no matching overload",
		`launch.limit`:                                          "must be true or false",
		`window(now, "Mon-Thu 20:00", "Europe/Zurich")`:         "HH:MM-HH:MM",
		`window(now, "Mon-Fri 08:00-18:00", "Europe/Zürich")`:   "Unknown time zone",
		`window(now, "Mon-Funday 08:00-18:00", "UTC")`:          "unknown day",
		"": "empty",
		`launch.template.organization == "Ops" && launch.workflow.id == 0 && !launch.user.superuser`: "",
	} {
		err := Check(expr)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", expr, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", expr, err, want)
		}
	}
}

func TestRefuses(t *testing.T) {
	wed := time.Date(2026, 11, 18, 21, 30, 0, 0, time.UTC) // 22:30 in Zurich, a Wednesday
	for expr, want := range map[string]bool{
		`launch.inventory.name == "prod" && launch.limit == ""`: true,
		`launch.inventory.hosts > 100 && launch.limit == ""`:    true,
		`launch.verbosity >= 3`:                                 false,
		`!launch.image.startsWith("registry.example.com/")`:     true,
		`launch.inventory.name == "prod" && !window(now, "Mon-Thu 20:00-06:00", "Europe/Zurich") && !("oncall" in launch.user.teams)`: false,
		`window(now, "Mon-Thu 20:00-06:00", "Europe/Zurich")`:                                                                         true,
		`window(now, "Wed 09:00-17:00")`:                  false,
		`launch.extra_vars.password == "$encrypted$"`:     true,
		`launch.credentials.exists(c, c.kind == "cloud")`: false,
	} {
		got, err := Refuses(context.Background(), expr, sample(), wed)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v", expr, got, err)
		}
	}
	if _, err := Refuses(context.Background(), `launch.extra_vars.missing == "x"`, sample(), wed); err == nil {
		t.Error("a missing extra var should be an error")
	}
}

func TestInWindow(t *testing.T) {
	zurich, _ := time.LoadLocation("Europe/Zurich")
	at := func(day, hour int) time.Time { return time.Date(2026, 11, 16+day, hour, 0, 0, 0, zurich) } // 16 is a Monday
	for _, c := range []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"Mon-Thu 20:00-06:00", at(0, 21), true},
		{"Mon-Thu 20:00-06:00", at(1, 5), true},   // Tuesday morning, from Monday night
		{"Mon-Thu 20:00-06:00", at(4, 5), true},   // Friday morning, from Thursday night
		{"Mon-Thu 20:00-06:00", at(4, 21), false}, // Friday night
		{"Mon-Thu 20:00-06:00", at(0, 5), false},  // Monday morning: Sunday night isn't in
		{"Mon-Thu 20:00-06:00", at(2, 12), false},
		{"Sat,Sun", at(5, 12), true},
		{"Sat,Sun", at(4, 23), false},
		{"Fri-Mon", at(6, 3), true},
		{"22:00-06:00", at(3, 23), true},
		{"Mon-Fri 08:00-12:00; Mon-Fri 13:00-17:00", at(2, 12), false},
		{"Mon-Fri 08:00-12:00; Mon-Fri 13:00-17:00", at(2, 13), true},
		{"Mon 00:00-24:00", at(0, 23), true},
	} {
		got, err := InWindow(c.t, c.spec, "Europe/Zurich")
		if err != nil || got != c.want {
			t.Errorf("%s at %s: %v, %v", c.spec, c.t.Format("Mon 15:04"), got, err)
		}
	}
}

func TestFields(t *testing.T) {
	paths := map[string]string{}
	for _, f := range Fields() {
		paths[f.Path] = f.Type
	}
	for p, typ := range map[string]string{"launch.inventory.hosts": "int", "launch.user.teams": "list of string",
		"launch.credentials[].kind": "string", "launch.extra_vars": "map", "now": "timestamp"} {
		if paths[p] != typ {
			t.Errorf("%s: %q, want %q", p, paths[p], typ)
		}
	}
}

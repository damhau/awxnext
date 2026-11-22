package bench

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReportsOptions sizes awxnext's report history. The rows go straight into
// report_runs and report_host_days: running a million jobs to fill them
// would measure the job runner, not the reports.
type ReportsOptions struct {
	DatabaseURL string
	// Runs over Days, of Templates templates in two organizations.
	Runs, Days, Templates int
	// Hosts, each run every day by HostTemplates templates.
	Hosts, HostTemplates int
	// Requests per report, period and user measured.
	Requests int
	Log      func(format string, args ...any)
}

// ReportsResult is the history's size and each report's response times.
type ReportsResult struct {
	Runs        int64            `json:"runs"`
	HostDays    int64            `json:"host_days"`
	SeedSeconds float64          `json:"seed_seconds"`
	Latencies   []ReportsLatency `json:"latencies"`
}

// ReportsLatency is one report over one period, for one user.
type ReportsLatency struct {
	Report string  `json:"report"`
	Days   int     `json:"days"`
	User   string  `json:"user"`
	MS     Latency `json:"ms"`
}

// The bench's rows are told apart by these: no real job or template has
// such an ID.
const (
	benchJobBase      = 1 << 40
	benchTemplateBase = 1 << 39
)

// Reports seeds a report history in two organizations and measures each
// report for the admin and for a user with an organization role in one of
// them, who reads half the history through the jobs' rights.
func Reports(ctx context.Context, t Target, o ReportsOptions) (*ReportsResult, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	c := newClient(t)
	stamp := time.Now().Format("20060102-150405")
	orgA, err := c.create(ctx, "organizations/", map[string]any{"name": "bench-reports-a-" + stamp})
	if err != nil {
		return nil, err
	}
	orgB, err := c.create(ctx, "organizations/", map[string]any{"name": "bench-reports-b-" + stamp})
	if err != nil {
		return nil, err
	}
	user := "bench-reports-" + stamp
	uid, err := c.create(ctx, "users/", map[string]any{"username": user, "password": t.Password})
	if err != nil {
		return nil, err
	}
	var roles struct {
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
	}
	if _, err := c.do(ctx, "GET", "role_definitions/?name="+url.QueryEscape("Organization Execute"), nil, &roles); err != nil || len(roles.Results) == 0 {
		return nil, fmt.Errorf("the Organization Execute role: %v", err)
	}
	if _, err := c.create(ctx, "role_user_assignments/", map[string]any{"role_definition": roles.Results[0].ID, "user": uid, "object_id": orgA}); err != nil {
		return nil, err
	}

	db, err := pgx.Connect(ctx, o.DatabaseURL)
	if err != nil {
		return nil, err
	}
	defer db.Close(ctx)
	r := &ReportsResult{}
	start := time.Now()
	o.Log("seeding %d runs over %d days", o.Runs, o.Days)
	if _, err := db.Exec(ctx, `INSERT INTO report_runs (job_id, unified_job_template_id, name, organization_id, project_id, launch_type,
			created_by_id, status, started, finished, elapsed)
		SELECT $1::bigint + g, $2::bigint + g % $5::int, 'bench-template-' || g % $5::int, CASE WHEN g % $5::int % 2 = 0 THEN $3::bigint ELSE $4::bigint END,
			$2::bigint + g % 20, (ARRAY['manual', 'scheduled', 'webhook', 'workflow'])[1 + g % 4], CASE WHEN g % 4 = 0 THEN $8::bigint END,
			CASE WHEN x < 0.9 THEN 'successful' WHEN x < 0.98 THEN 'failed' ELSE 'canceled' END,
			f - make_interval(secs => e), f, e
		FROM generate_series(1, $6::bigint) g,
			LATERAL (SELECT random() x, now() - (g::float8 / $6::bigint) * make_interval(days => $7::int) f, 20 + random() * 300 e) v`,
		benchJobBase, benchTemplateBase, orgA, orgB, o.Templates, o.Runs, o.Days, uid); err != nil {
		return nil, err
	}
	o.Log("seeding %d hosts × %d templates × %d days", o.Hosts, o.HostTemplates, o.Days)
	if _, err := db.Exec(ctx, `INSERT INTO report_host_days (day, host_name, unified_job_template_id, organization_id, launch_type, runs, failed, unreachable)
		SELECT d, 'bench-host-' || h, $1::bigint + (h + k) % $4::int, CASE WHEN (h + k) % $4::int % 2 = 0 THEN $2::bigint ELSE $3::bigint END, 'scheduled',
			1 + (random() * 3)::int, (random() < 0.03)::int, (random() < 0.01)::int
		FROM generate_series(current_date - $5::int + 1, current_date, interval '1 day') d, generate_series(1, $6::int) h, generate_series(1, $7::int) k`,
		benchTemplateBase, orgA, orgB, o.Templates, o.Days, o.Hosts, o.HostTemplates); err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, `ANALYZE report_runs; ANALYZE report_host_days`); err != nil {
		return nil, err
	}
	r.SeedSeconds = time.Since(start).Seconds()
	db.QueryRow(ctx, `SELECT count(*) FROM report_runs WHERE job_id >= $1`, benchJobBase).Scan(&r.Runs)
	db.QueryRow(ctx, `SELECT count(*) FROM report_host_days WHERE organization_id IN ($1, $2)`, orgA, orgB).Scan(&r.HostDays)
	o.Log("seeded %d runs and %d host days in %.0f s", r.Runs, r.HostDays, r.SeedSeconds)

	users := []struct {
		name, query string
		c           *client
	}{
		{"admin, one organization", fmt.Sprintf("&organization=%d", orgA), c},
		{"admin, both", "", c},
		{"organization role", "", newClient(Target{URL: t.URL, User: user, Password: t.Password, Insecure: t.Insecure})},
	}
	today := time.Now().UTC()
	for _, days := range []int{30, 90, o.Days} {
		from := today.AddDate(0, 0, 1-days).Format(time.DateOnly)
		for _, rep := range []string{"totals", "templates", "trends", "hosts", "launches"} {
			for _, u := range users {
				path := fmt.Sprintf("reports/%s/?from=%s%s", rep, from, u.query)
				var ms []float64
				for range o.Requests {
					t0 := time.Now()
					if _, err := u.c.do(ctx, "GET", path, nil, nil); err != nil {
						return nil, err
					}
					ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
				}
				l := ReportsLatency{Report: rep, Days: days, User: u.name, MS: latency(ms)}
				o.Log("%s, %d days, %s: p50 %.0f ms, p95 %.0f ms", rep, days, u.name, l.MS.P50, l.MS.P95)
				r.Latencies = append(r.Latencies, l)
			}
		}
	}
	return r, nil
}

// CleanReports deletes the rows Reports seeded, not its organizations and
// user.
func CleanReports(ctx context.Context, databaseURL string) (int64, error) {
	db, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return 0, err
	}
	defer db.Close(ctx)
	runs, err := db.Exec(ctx, `DELETE FROM report_runs WHERE job_id >= $1`, benchJobBase)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, table := range []string{"report_host_days", "report_keys"} {
		tag, err := db.Exec(ctx, `DELETE FROM `+table+` WHERE unified_job_template_id >= $1 AND unified_job_template_id < $2`, benchTemplateBase, benchJobBase)
		if err != nil {
			return 0, err
		}
		n += tag.RowsAffected()
	}
	return runs.RowsAffected() + n, nil
}

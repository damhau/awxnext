package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestEventArchive(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	ctx := context.Background()
	jt := f.template(t, "site.yml", nil)
	j1 := f.launch(t, a, jt, nil)
	mustStatus(t, f, j1, "successful")
	j2 := f.launch(t, a, jt, nil)
	mustStatus(t, f, j2, "successful")
	before := a.get(fmt.Sprintf("jobs/%d/job_events/?order_by=counter&page_size=200", j1.id()))
	firstID := before.results()[0]["id"]

	// Age the jobs: j1 into the default partition, j2 into an old daily partition.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := app.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("UPDATE unified_jobs SET created = now() - interval '30 days' WHERE id = $1", j1.id())
	exec("UPDATE job_events SET job_created = now() - interval '30 days' WHERE job_id = $1", j1.id())
	exec("CREATE TABLE IF NOT EXISTS job_events_20200101 PARTITION OF job_events FOR VALUES FROM ('2020-01-01 00:00:00+00') TO ('2020-01-02 00:00:00+00')")
	exec("UPDATE unified_jobs SET created = '2020-01-01 12:00:00+00' WHERE id = $1", j2.id())
	exec("UPDATE job_events SET job_created = '2020-01-01 12:00:00+00' WHERE job_id = $1", j2.id())

	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	count := func(sql string, args ...any) int {
		var n int
		if err := app.DB.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, id := range []int64{j1.id(), j2.id()} {
		if n := count("SELECT count(*) FROM job_events WHERE job_id = $1", id); n != 0 {
			t.Fatalf("job %d still has %d events", id, n)
		}
		if n := count("SELECT event_count FROM job_event_archives WHERE job_id = $1", id); n == 0 {
			t.Fatalf("job %d not archived", id)
		}
	}
	if n := count("SELECT count(*) FROM pg_class WHERE relname = 'job_events_20200101'"); n != 0 {
		t.Fatal("old partition not dropped")
	}

	// Reading output restores the events with their ids.
	if out := f.stdout(t, j1.id()); !strings.Contains(out, "hello nobody from h1") {
		t.Fatalf("restored stdout:\n%s", out)
	}
	after := a.get(fmt.Sprintf("jobs/%d/job_events/?order_by=counter&page_size=200", j1.id()))
	if after.count() != before.count() || after.results()[0]["id"] != firstID {
		t.Fatalf("restored events: %d vs %d", after.count(), before.count())
	}
	if r := a.get(fmt.Sprintf("jobs/%d/job_events/", j2.id())); r.count() == 0 {
		t.Fatal("events of the dropped partition not restored")
	}
	// A second archive pass leaves restored events alone until they expire.
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM job_events WHERE job_id = $1", j1.id()); n != before.count() {
		t.Fatalf("restored events removed early: %d", n)
	}
	exec("UPDATE job_event_archives SET restored = now() - interval '2 days' WHERE job_id = $1", j1.id())
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM job_events WHERE job_id = $1", j1.id()); n != 0 {
		t.Fatalf("expired restore not cleaned: %d", n)
	}
	if out := f.stdout(t, j1.id()); !strings.Contains(out, "hello nobody from h1") {
		t.Fatal("second restore failed")
	}
	// Deleting the job removes its archive.
	must(t, a.delete(fmt.Sprintf("jobs/%d/", j2.id())), 204)
	if n := count("SELECT count(*) FROM job_event_archives WHERE job_id = $1", j2.id()); n != 0 {
		t.Fatal("archive kept after job deletion")
	}
}

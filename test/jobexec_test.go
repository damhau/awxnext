package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/jobexec"
)

// fakeJobExec stands in for awxnext-jobexec, as the same user: it records
// its calls and runs the command in the directory it makes, in a session of
// its own that SIGTERM kills.
const fakeJobExec = `#!/bin/sh
[ "$1" = run ] || exit 2
user=$2 dir=$3
shift 3
echo "run $user $dir HOME=$HOME TMPDIR=$TMPDIR $*" >> LOG
mkdir -m 700 "$dir" "$dir/tmp" || exit 125
cd "$dir" || exit 125
exec 3<&0
setsid "$@" <&3 3<&- &
child=$!
trap 'echo "TERM $user" >> LOG; kill -9 -$child' TERM
wait $child
rc=$?
wait $child 2>/dev/null
rm -rf "$dir"
echo "done $user" >> LOG
exit $rc
`

// Jobs on runtime none go through the job users' helper when it works:
// the worker's command, home and temporary directory, a job user per
// running job, and SIGTERM to the helper to cancel (it kills the job user's
// processes, which the server cannot). awxnext-jobexec itself, as job users
// with its file capabilities, is tested on the all-in-one image
// (test/isolation).
func TestJobUsersHelper(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	helper := filepath.Join(dir, "awxnext-jobexec")
	if err := os.WriteFile(helper, []byte(strings.ReplaceAll(fakeJobExec, "LOG", log)), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	homes := filepath.Join(dir, "open", "jobs", "home")
	var lines []string
	for _, n := range []int{0, 1, 3} {
		lines = append(lines, fmt.Sprintf("awxjob%d:x:%d:%d::%s/awxjob%d:/usr/sbin/nologin", n, 50000+n, 50000+n, homes, n))
	}
	os.WriteFile(passwd, []byte("root:x:0:0::/root:/bin/sh\n"+strings.Join(lines, "\n")+"\n"), 0o644)
	if _, err := jobexec.Setup(context.Background(), "auto", filepath.Join(dir, "missing"), filepath.Join(dir, "jobs"), filepath.Join(dir, "projects"), passwd); err == nil {
		t.Fatal("isolation without its helper")
	}
	// A directory above the roots that others can write to lets a job put
	// its own in their place: the server's own are fixed (one that is not,
	// a Kubernetes emptyDir, makes Setup fail).
	open := filepath.Join(dir, "open")
	os.Mkdir(open, 0o777)
	os.Chmod(open, 0o777)
	iso, err := jobexec.Setup(context.Background(), "auto", helper, filepath.Join(open, "jobs"), filepath.Join(open, "projects"), passwd)
	if fi, _ := os.Stat(open); err != nil || fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("Setup under a directory others can write to: %v, left %v", err, fi.Mode())
	}
	if err != nil {
		t.Fatal(err)
	}
	// awxjob3 is not one: the users go up to the first missing slot.
	if len(iso.Users) != 2 {
		t.Fatalf("job users: %+v", iso.Users)
	}
	jobexec.Default = iso
	t.Cleanup(func() { jobexec.Default = nil })
	calls := func() string { b, _ := os.ReadFile(log); return string(b) }

	job := f.launch(t, a, f.template(t, "site.yml", nil), nil)
	mustStatus(t, f, job, "successful")
	cwd := fmt.Sprint(job.Body["job_cwd"])
	jobDir := strings.TrimSuffix(cwd, "/pdd/project")
	if filepath.Dir(jobDir) != iso.Root || !strings.HasPrefix(filepath.Base(jobDir), fmt.Sprintf("%d-", job.id())) {
		t.Fatalf("job_cwd %s, not in a directory of its own in %s", cwd, iso.Root)
	}
	want := fmt.Sprintf("run awxjob0 %s HOME=%s/awxjob0 TMPDIR=%s/tmp ansible-runner worker --private-data-dir=%s/pdd", jobDir, homes, jobDir, jobDir)
	if c := calls(); !strings.Contains(c, want) || !strings.Contains(c, "done awxjob0") {
		t.Fatalf("helper calls, want %q:\n%s", want, c)
	}

	// Two at a time: a job user each; canceled through the helper.
	slow := f.template(t, "slow.yml", map[string]any{"allow_simultaneous": true})
	var ids []int64
	for range 2 {
		ids = append(ids, must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", slow), nil), 201).id())
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		running := 0
		for _, id := range ids {
			if a.get(fmt.Sprintf("jobs/%d/", id)).Body["status"] == "running" && a.get(fmt.Sprintf("jobs/%d/job_events/", id)).count() > 0 {
				running++
			}
		}
		if running == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("both jobs did not start:\n%s", calls())
		}
	}
	for _, id := range ids {
		must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", id), nil), 202)
	}
	for _, id := range ids {
		if j := waitJob(t, a, fmt.Sprintf("jobs/%d/", id)); j.Body["status"] != "canceled" {
			t.Fatalf("cancel: %v", j.Body["status"])
		}
	}
	c := calls()
	for _, u := range []string{"awxjob0", "awxjob1"} {
		if !strings.Contains(c, "TERM "+u) || strings.Count(c, "run "+u+" ") < 1 {
			t.Fatalf("%s did not run a slow job and get SIGTERM:\n%s", u, c)
		}
	}
	// Both are free again, and the jobs' directories gone.
	job = f.launch(t, a, f.template(t, "site.yml", nil), nil)
	mustStatus(t, f, job, "successful")
	if entries, _ := os.ReadDir(iso.Root); len(entries) != 0 {
		t.Fatalf("left in %s: %v\n%s", iso.Root, entries, calls())
	}
}

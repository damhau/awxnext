package test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ulikunitz/xz"
)

// tarMember is a tar entry; link makes it a symlink, hard a hard link.
type tarMember struct {
	name, body, link string
	hard             bool
}

func tarball(t *testing.T, compress string, members ...tarMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w interface {
		Write([]byte) (int, error)
		Close() error
	}
	switch compress {
	case "gz":
		w = gzip.NewWriter(&buf)
	case "xz":
		x, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w = x
	}
	var tw *tar.Writer
	if w != nil {
		tw = tar.NewWriter(w)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(m.name, "/"):
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		case m.link != "" && m.hard:
			h.Typeflag, h.Linkname, h.Size = tar.TypeLink, m.link, 0
		case m.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.link, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(m.body))
	}
	tw.Close()
	if w != nil {
		w.Close()
	}
	return buf.Bytes()
}

func zipball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte(files[n]))
	}
	zw.Close()
	return buf.Bytes()
}

func sha1Hex(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

// archiveServer serves archives by path; /private/ needs basic auth.
type archiveServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
}

func newArchiveServer(t *testing.T) *archiveServer {
	s := &archiveServer{files: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/private/") {
			if u, p, ok := r.BasicAuth(); !ok || u != "reader" || p != "s3cret" {
				w.Header().Set("WWW-Authenticate", `Basic realm="archives"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if r.URL.Path == "/moved.tar.gz" {
			http.Redirect(w, r, "/v1.tar.gz", http.StatusFound)
			return
		}
		s.mu.Lock()
		b, ok := s.files[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *archiveServer) put(path string, b []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = b
	return s.URL + path
}

func TestArchiveProjects(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	srv := newArchiveServer(t)
	v1 := tarball(t, "gz",
		tarMember{name: "test-playbooks-1.0.0/"},
		tarMember{name: "test-playbooks-1.0.0/site.yml", body: sitePlaybook},
		tarMember{name: "test-playbooks-1.0.0/plays/other.yml", body: helloPlaybook},
		tarMember{name: "test-playbooks-1.0.0/plays/same.yml", link: "other.yml"},
		tarMember{name: "test-playbooks-1.0.0/plays/copy.yml", link: "test-playbooks-1.0.0/plays/other.yml", hard: true},
		tarMember{name: "test-playbooks-1.0.0/passwd", link: "/etc/passwd"},
		tarMember{name: "test-playbooks-1.0.0/up", link: "../../outside"},
	)
	url := srv.put("/v1.tar.gz", v1)
	create := func(body map[string]any) resp {
		b := map[string]any{"name": uniq("archive"), "organization": f.org, "scm_type": "archive"}
		for k, v := range body {
			b[k] = v
		}
		return a.post("projects/", b)
	}

	// AWX's rules: http or https only, no branch, no refspec or submodules.
	for field, body := range map[string]map[string]any{
		"scm_url":              {"scm_url": "file:///tmp/x.tar.gz"},
		"scm_branch":           {"scm_url": url, "scm_branch": "main"},
		"scm_refspec":          {"scm_url": url, "scm_refspec": "refs/pull/*"},
		"scm_track_submodules": {"scm_url": url, "scm_track_submodules": true},
	} {
		if r := create(body); r.Status != 400 || r.Body[field] == nil {
			t.Errorf("%s: %d %s", field, r.Status, r.Raw)
		}
	}
	if r := create(map[string]any{"scm_url": "ftp://example.com/x.tar.gz"}); r.Status != 400 {
		t.Errorf("ftp: %d %s", r.Status, r.Raw)
	}
	if r := must(t, a.do("OPTIONS", "projects/", nil), 200); !strings.Contains(r.Raw, `"Remote Archive"`) {
		t.Errorf("scm_type choices: %s", r.Raw)
	}

	// Sync: the revision is the archive's SHA-1, its top directory is removed,
	// links that stay inside are kept.
	pid := must(t, create(map[string]any{"scm_url": url}), 201).id()
	pu := latestUpdate(t, a, pid)
	if pu.Body["status"] != "successful" || pu.Body["scm_revision"] != sha1Hex(v1) {
		t.Fatalf("sync: %v %v %v", pu.Body["status"], pu.Body["scm_revision"], pu.Body["job_explanation"])
	}
	p := must(t, a.get(fmt.Sprintf("projects/%d/", pid)), 200).Body
	local := filepath.Join(app.Cfg.ProjectsRoot, p["local_path"].(string))
	for name, want := range map[string]string{"site.yml": sitePlaybook, "plays/same.yml": helloPlaybook, "plays/copy.yml": helloPlaybook} {
		if b, err := os.ReadFile(filepath.Join(local, name)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
	for _, name := range []string{"passwd", "up", "test-playbooks-1.0.0"} {
		if _, err := os.Lstat(filepath.Join(local, name)); err == nil {
			t.Errorf("%s should not be unpacked", name)
		}
	}
	pbs := must(t, a.get(fmt.Sprintf("projects/%d/playbooks/", pid)), 200).Raw
	if !strings.Contains(pbs, "site.yml") || !strings.Contains(pbs, "plays/other.yml") {
		t.Errorf("playbooks: %s", pbs)
	}
	out := a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())).Raw
	if !strings.Contains(out, "Skipping passwd: its link leaves the project") {
		t.Errorf("stdout: %s", out)
	}

	// The same archive again: the same revision, already unpacked.
	up := must(t, a.post(fmt.Sprintf("projects/%d/update/", pid), nil), 202)
	pu = waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64))))
	if pu.Body["scm_revision"] != sha1Hex(v1) || !strings.Contains(a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())).Raw, "already unpacked") {
		t.Errorf("same archive: %v", pu.Body["scm_revision"])
	}

	// Update on launch picks up a changed archive through a dependency.
	v2 := tarball(t, "xz",
		tarMember{name: "test-playbooks-2.0.0/site.yml", body: sitePlaybook + "# v2\n"},
		tarMember{name: "test-playbooks-2.0.0/plays/other.yml", body: helloPlaybook},
	)
	srv.put("/v1.tar.gz", v2)
	must(t, a.patch(fmt.Sprintf("projects/%d/", pid), map[string]any{"scm_update_on_launch": true}), 200)
	f.project = pid
	jt := f.template(t, "site.yml", nil)
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if job.Body["scm_revision"] != sha1Hex(v2) {
		t.Errorf("job ran %v, want %v", job.Body["scm_revision"], sha1Hex(v2))
	}
	if deps := must(t, a.get(fmt.Sprintf("project_updates/?unified_job_template=%d&launch_type=dependency", pid)), 200); deps.count() != 1 {
		t.Errorf("dependency updates: %d", deps.count())
	}
	if b, _ := os.ReadFile(filepath.Join(local, "site.yml")); !strings.Contains(string(b), "# v2") {
		t.Error("local path not switched to the changed archive")
	}
	if _, err := os.Stat(filepath.Join(app.Cfg.ProjectsRoot, ".revisions", fmt.Sprint(pid), sha1Hex(v1), "site.yml")); err != nil {
		t.Errorf("the previous revision is kept for running jobs: %v", err)
	}
	// Without update on launch, jobs keep the synced revision.
	must(t, a.patch(fmt.Sprintf("projects/%d/", pid), map[string]any{"scm_update_on_launch": false}), 200)
	srv.put("/v1.tar.gz", v1)
	job = f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if job.Body["scm_revision"] != sha1Hex(v2) {
		t.Errorf("job without update on launch ran %v", job.Body["scm_revision"])
	}

	// A zip without a top directory, behind a redirect.
	srv.put("/v1.tar.gz", zipball(t, map[string]string{"site.yml": helloPlaybook, "roles/r/tasks/main.yml": "[]\n"}))
	zp := must(t, create(map[string]any{"scm_url": srv.URL + "/moved.tar.gz"}), 201).id()
	if pu := latestUpdate(t, a, zp); pu.Body["status"] != "successful" {
		t.Errorf("zip: %v %v", pu.Body["status"], pu.Body["job_explanation"])
	}
	zl := filepath.Join(app.Cfg.ProjectsRoot, must(t, a.get(fmt.Sprintf("projects/%d/", zp)), 200).Body["local_path"].(string))
	if _, err := os.Stat(filepath.Join(zl, "roles/r/tasks/main.yml")); err != nil {
		t.Errorf("zip content: %v", err)
	}

	// Credentials: the username and password go with basic auth.
	private := srv.put("/private/p.tar.gz", tarball(t, "", tarMember{name: "site.yml", body: helloPlaybook}))
	if pu := latestUpdate(t, a, must(t, create(map[string]any{"scm_url": private}), 201).id()); pu.Body["status"] != "failed" ||
		!strings.Contains(pu.Body["job_explanation"].(string), "401") {
		t.Errorf("no credential: %v %v", pu.Body["status"], pu.Body["job_explanation"])
	}
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("scm"), "organization": f.org, "credential_type": credType(t, a, "scm"),
		"inputs": map[string]any{"username": "reader", "password": "s3cret"}}), 201).id()
	if pu := latestUpdate(t, a, must(t, create(map[string]any{"scm_url": private, "credential": cred}), 201).id()); pu.Body["status"] != "successful" {
		t.Errorf("with credential: %v %v", pu.Body["status"], pu.Body["job_explanation"])
	}

	// Members outside the project and files that are not archives fail the sync.
	for name, c := range map[string]struct {
		body []byte
		msg  string
	}{
		"traversal":   {tarball(t, "gz", tarMember{name: "p/site.yml", body: "x"}, tarMember{name: "../../evil.yml", body: "x"}), `The archive member "../../evil.yml" is outside the project.`},
		"absolute":    {tarball(t, "", tarMember{name: "/etc/cron.d/x", body: "x"}), "is outside the project"},
		"not archive": {[]byte("<html>not found</html>"), "The downloaded file is not a zip or tar archive."},
	} {
		u := srv.put("/bad-"+strings.ReplaceAll(name, " ", "-"), c.body)
		pu := latestUpdate(t, a, must(t, create(map[string]any{"scm_url": u}), 201).id())
		if pu.Body["status"] != "failed" || !strings.Contains(pu.Body["job_explanation"].(string), c.msg) {
			t.Errorf("%s: %v %v", name, pu.Body["status"], pu.Body["job_explanation"])
		}
	}
	if pu := latestUpdate(t, a, must(t, create(map[string]any{"scm_url": srv.URL + "/missing.zip"}), 201).id()); pu.Body["status"] != "failed" ||
		!strings.Contains(pu.Body["job_explanation"].(string), "404") {
		t.Errorf("missing: %v %v", pu.Body["status"], pu.Body["job_explanation"])
	}
}

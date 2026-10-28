// Package gitsync syncs git projects natively on the control plane (no
// execution environment needed). Each project keeps one fetch-only clone per
// node; every revision is checked out once into an immutable worktree keyed by
// commit SHA and shared by all jobs using it.
package gitsync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/projectscan"
	"github.com/damhau/awxnext/internal/resources"
)

// Register installs the project update job type.
func Register() {
	jobs.Register(&jobs.TypeSpec{
		Type:         "project_update",
		TemplateType: "project",
		Runner:       jobs.RunnerFunc(runProjectUpdate),
		ControlPlane: true,
		Serial:       true,
	})
}

// Source describes what to fetch.
type Source struct {
	ProjectID       int64
	URL             string
	Branch          string
	Refspec         string
	CredentialID    int64
	TrackSubmodules bool
	DeleteOnUpdate  bool
	// Clean (scm_clean) discards changes made on disk to a reused checkout.
	Clean bool
}

var (
	locksMu sync.Mutex
	locks   = map[int64]*sync.Mutex{}
)

func projectLock(id int64) *sync.Mutex {
	locksMu.Lock()
	defer locksMu.Unlock()
	if locks[id] == nil {
		locks[id] = &sync.Mutex{}
	}
	return locks[id]
}

// RepoDir is the fetch clone of a project on this node.
func RepoDir(root string, project int64) string {
	return resources.ProjectRepoDir(root, project)
}

// RevisionDir is the immutable checkout of one revision on this node.
func RevisionDir(root string, project int64, sha string) string {
	return filepath.Join(root, ".revisions", fmt.Sprint(project), sha)
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Sync fetches the source and ensures a checkout of the requested branch, tag
// or commit exists. It returns the commit SHA and its checkout directory.
func Sync(ctx context.Context, app *api.App, src Source, logf func(string)) (string, string, error) {
	lock := projectLock(src.ProjectID)
	lock.Lock()
	defer lock.Unlock()

	root := app.Cfg.ProjectsRoot
	repo := RepoDir(root, src.ProjectID)
	env, cleanup, err := gitEnv(ctx, app, src.CredentialID)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	git := func(dir string, args ...string) (string, error) {
		return run(ctx, dir, env, logf, args...)
	}
	if src.DeleteOnUpdate {
		logf("Deleting project checkout before update")
		os.RemoveAll(repo)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			return "", "", err
		}
		if _, err := git(repo, "init", "--quiet"); err != nil {
			return "", "", err
		}
		if _, err := git(repo, "remote", "add", "origin", src.URL); err != nil {
			return "", "", err
		}
	} else if _, err := git(repo, "remote", "set-url", "origin", src.URL); err != nil {
		return "", "", err
	}
	fetch := []string{"fetch", "--prune", "--force", "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*"}
	if src.Refspec != "" {
		fetch = append(fetch, strings.Fields(src.Refspec)...)
	}
	if _, err := git(repo, fetch...); err != nil {
		return "", "", fmt.Errorf("fetching %s failed", redact(src.URL))
	}
	branch := src.Branch
	if branch == "" {
		out, err := git(repo, "ls-remote", "--symref", "origin", "HEAD")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "ref: refs/heads/") {
					branch = strings.Fields(strings.TrimPrefix(line, "ref: refs/heads/"))[0]
				}
			}
		}
		if branch == "" {
			branch = "main"
		}
	}
	sha, err := resolve(ctx, repo, env, branch)
	if err != nil && shaRe.MatchString(branch) {
		// A commit not on any branch: fetch it directly.
		if _, ferr := git(repo, "fetch", "origin", branch); ferr == nil {
			sha, err = resolve(ctx, repo, env, branch)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("branch, tag or commit %q not found", branch)
	}
	logf(fmt.Sprintf("Revision %s (%s)", sha, branch))
	dir := RevisionDir(root, src.ProjectID, sha)
	if _, err := os.Stat(dir); err != nil {
		os.MkdirAll(filepath.Dir(dir), 0o755)
		git(repo, "worktree", "prune")
		tmp := dir + ".tmp"
		os.RemoveAll(tmp)
		if _, err := git(repo, "worktree", "add", "--detach", "--force", tmp, sha); err != nil {
			return "", "", err
		}
		if _, err := os.Stat(filepath.Join(tmp, ".gitmodules")); err == nil {
			args := []string{"submodule", "update", "--init", "--recursive"}
			if src.TrackSubmodules {
				args = append(args, "--remote")
			}
			if _, err := git(tmp, args...); err != nil {
				return "", "", err
			}
		}
		if err := os.Rename(tmp, dir); err != nil {
			return "", "", err
		}
		git(repo, "worktree", "repair", dir)
	} else {
		logf("Revision already checked out on this node")
		// Jobs work on a copy, so only a change made on disk can show here.
		if src.Clean {
			if out, err := git(dir, "status", "--porcelain", "--untracked-files=all"); err == nil && strings.TrimSpace(out) != "" {
				logf("Discarding local changes (scm_clean)")
				if _, err := git(dir, "reset", "--hard", "--quiet", sha); err != nil {
					return "", "", err
				}
				if _, err := git(dir, "clean", "-ffdxq"); err != nil {
					return "", "", err
				}
			}
		}
	}
	return sha, dir, nil
}

func resolve(ctx context.Context, repo string, env []string, ref string) (string, error) {
	for _, cand := range []string{"refs/remotes/origin/" + ref, "refs/tags/" + ref, ref} {
		out, err := run(ctx, repo, env, nil, "rev-parse", "--verify", "--quiet", cand+"^{commit}")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", errors.New("not found")
}

var userinfoRe = regexp.MustCompile(`://[^/@]+@`)

func redact(s string) string { return userinfoRe.ReplaceAllString(s, "://****@") }

// run executes git, streaming its output to logf.
func run(ctx context.Context, dir string, env []string, logf func(string), args ...string) (string, error) {
	if logf != nil {
		logf("$ git " + redact(strings.Join(args, " ")))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			out.WriteString(line + "\n")
			if logf != nil {
				logf(redact(line))
			}
		}
		close(done)
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	return out.String(), err
}

// gitEnv prepares environment variables for credentials.
func gitEnv(ctx context.Context, app *api.App, credID int64) ([]string, func(), error) {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null",
		"GIT_ASKPASS=/bin/false",
	}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "GIT_SSL_CAINFO"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	noop := func() {}
	if credID == 0 {
		return env, noop, nil
	}
	rc, err := resources.ResolveCredential(ctx, app, credID)
	if err != nil {
		return nil, noop, err
	}
	tmp, err := os.MkdirTemp("", "awxnext-git-")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	in := func(k string) string { s, _ := rc.Inputs[k].(string); return s }
	if key := in("ssh_key_data"); key != "" {
		keyFile := filepath.Join(tmp, "id")
		if err := os.WriteFile(keyFile, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
			cleanup()
			return nil, noop, err
		}
		if pass := in("ssh_key_unlock"); pass != "" {
			cmd := exec.CommandContext(ctx, "ssh-keygen", "-p", "-P", pass, "-N", "", "-f", keyFile)
			if out, err := cmd.CombinedOutput(); err != nil {
				cleanup()
				return nil, noop, fmt.Errorf("unlocking SSH key failed: %s", strings.TrimSpace(string(out)))
			}
		}
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+keyFile+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null")
	}
	if user, pass := in("username"), in("password"); user != "" || pass != "" {
		askpass := filepath.Join(tmp, "askpass")
		script := "#!/bin/sh\ncase \"$1\" in\n  Username*) printf '%s' \"$AWX_GIT_USERNAME\" ;;\n  *) printf '%s' \"$AWX_GIT_PASSWORD\" ;;\nesac\n"
		if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
			cleanup()
			return nil, noop, err
		}
		env = append(env, "GIT_ASKPASS="+askpass, "AWX_GIT_USERNAME="+user, "AWX_GIT_PASSWORD="+pass)
	}
	return env, cleanup, nil
}

func runProjectUpdate(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row := rc.Job
	app := rc.M.App
	if t := row.Int("timeout"); t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t)*time.Second)
		defer cancel()
	}
	if row.Str("scm_type") != "git" {
		rc.Out.Line("Manual projects do not need to be updated.")
		return jobs.Result{Status: "successful"}
	}
	src := Source{
		ProjectID:       row.Int("unified_job_template_id"),
		URL:             row.Str("scm_url"),
		Branch:          row.Str("scm_branch"),
		Refspec:         row.Str("scm_refspec"),
		CredentialID:    row.Int("credential_id"),
		TrackSubmodules: row.Bool("scm_track_submodules"),
		DeleteOnUpdate:  row.Bool("scm_delete_on_update"),
		Clean:           row.Bool("scm_clean"),
	}
	rc.Out.Line(fmt.Sprintf("Updating project from %s", redact(src.URL)))
	sha, dir, err := Sync(ctx, app, src, rc.Out.Line)
	if err != nil {
		rc.Out.Emit(jobs.Event{Event: "error", Stdout: "ERROR: " + err.Error(), Failed: true})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return jobs.Result{Status: "failed", Explanation: "Job terminated due to timeout"}
		}
		return jobs.Result{Status: "failed", Explanation: err.Error()}
	}
	files, err := projectscan.Scan(dir, app.Settings.Bool(ctx, "AWX_SHOW_PLAYBOOK_LINKS"))
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	rc.Out.Line(fmt.Sprintf("Found %d playbooks and %d inventory files", len(files.Playbooks), len(files.Inventories)))
	for _, req := range []string{"roles/requirements.yml", "roles/requirements.yaml", "collections/requirements.yml", "collections/requirements.yaml", "requirements.yml"} {
		if _, err := os.Stat(filepath.Join(dir, req)); err == nil {
			rc.Out.Line("Found " + req + "; content is installed in the execution environment when a job runs")
		}
	}
	if row.Str("job_type") == "check" {
		pid := row.Int("unified_job_template_id")
		if lp := row.Str("local_path"); lp != "" && !strings.Contains(lp, "/") {
			link := filepath.Join(app.Cfg.ProjectsRoot, lp)
			tmp := link + ".tmp"
			os.Remove(tmp)
			if err := os.Symlink(dir, tmp); err == nil {
				os.Rename(tmp, link)
			}
		}
		if _, err := app.DB.Exec(ctx, `UPDATE unified_job_templates SET scm_revision = $2, playbook_files = $3, inventory_files = $4 WHERE id = $1`,
			pid, sha, files.Playbooks, files.Inventories); err != nil {
			return jobs.Result{Status: "error", Explanation: err.Error()}
		}
		pruneRevisions(app.Cfg.ProjectsRoot, pid, sha, 10)
	}
	rc.Out.Line("Project update successful")
	return jobs.Result{Status: "successful", Update: map[string]any{"scm_revision": sha}}
}

// pruneRevisions keeps the newest checkouts of a project on this node.
func pruneRevisions(root string, project int64, current string, keep int) {
	base := filepath.Join(root, ".revisions", fmt.Sprint(project))
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type rev struct {
		name string
		mod  time.Time
	}
	var revs []rev
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() && e.Name() != current && !strings.HasSuffix(e.Name(), ".tmp") {
			revs = append(revs, rev{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].mod.After(revs[j].mod) })
	for i, r := range revs {
		if i >= keep-1 {
			os.RemoveAll(filepath.Join(base, r.name))
		}
	}
	exec.Command("git", "-C", RepoDir(root, project), "worktree", "prune").Run()
}

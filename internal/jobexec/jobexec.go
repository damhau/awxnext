// Package jobexec runs a job's ansible-runner worker as a job user of its
// own on the "none" runtime, so that jobs running at the same time cannot
// read each other's files, nor write to the server's (project checkouts,
// private data directories).
//
// The job users are awxjob0, awxjob1, ... in /etc/passwd (the all-in-one
// image has 128), each with a home in a directory the server owns (Ansible
// takes the local user's home from passwd, not HOME). awxnext-jobexec, the helper the server starts a worker
// with, is executable only by the server's group and has the file
// capabilities setuid, setgid, chown and kill (setcap). For one job it:
//
//  1. sets no_new_privs, so the job cannot gain privileges (setuid binaries);
//  2. kills what is left of the job user's processes (a slot is reused);
//  3. empties the job user's home (made if missing), and creates the job's
//     directory, both owned by the job user and 0700, in directories the
//     server owns and only it can write to;
//  4. runs the worker as the job user, with no supplementary groups, in a
//     process group, killed (the whole group) on SIGTERM or SIGINT;
//  5. then kills the job user's processes again, reaps them (it is their
//     subreaper), removes the directory and empties the home.
package jobexec

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Prefix is the job users' name prefix.
const Prefix = "awxjob"

// MinUID is the lowest uid a job user may have.
const MinUID = 10000

// User is a job user.
type User struct {
	Name     string
	UID, GID int
	Home     string
}

// Users reads the job users from a passwd file, by slot (awxjob0 first),
// up to the first missing one.
func Users(passwd string) ([]User, error) {
	f, err := os.Open(passwd)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	byName := map[string]User{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.Split(s.Text(), ":")
		if len(parts) < 6 || !strings.HasPrefix(parts[0], Prefix) {
			continue
		}
		uid, err1 := strconv.Atoi(parts[2])
		gid, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil || uid < MinUID || gid < MinUID {
			continue
		}
		byName[parts[0]] = User{Name: parts[0], UID: uid, GID: gid, Home: parts[5]}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	var out []User
	for i := 0; ; i++ {
		u, ok := byName[fmt.Sprintf("%s%d", Prefix, i)]
		if !ok {
			return out, nil
		}
		out = append(out, u)
	}
}

// Lookup returns the job user named name.
func Lookup(passwd, name string) (User, error) {
	users, err := Users(passwd)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.Name == name {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("%s is not a job user (%s<N>, uid %d or more)", name, Prefix, MinUID)
}

// Isolation hands out this node's job users, one per running worker.
type Isolation struct {
	// Helper is awxnext-jobexec; Root holds the jobs' directories.
	Helper, Root string
	Users        []User
	free         chan User
}

// Default is set at start when jobs run as job users.
var Default *Isolation

// Setup returns the node's job isolation: nil, nil with mode "off", an
// error when it is not available (no job users, no working helper, a
// directory job users could replace). Mode "auto" and "users" differ in how
// the caller takes that error.
func Setup(ctx context.Context, mode, helper, jobsRoot, projectsRoot, passwd string) (*Isolation, error) {
	if mode == "off" {
		return nil, nil
	}
	users, err := Users(passwd)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("no job users (%s0, %s1, ... in %s)", Prefix, Prefix, passwd)
	}
	if _, err := os.Stat(helper); err != nil {
		return nil, fmt.Errorf("no helper: %w", err)
	}
	for _, d := range []string{jobsRoot, projectsRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := notReplaceable(d); err != nil {
			return nil, err
		}
	}
	root := filepath.Join(jobsRoot, "work")
	// Job users go through these to their directory and home, without
	// listing them.
	parents := []string{jobsRoot, root}
	for _, u := range users {
		if !filepath.IsAbs(u.Home) || filepath.Base(u.Home) != u.Name {
			return nil, fmt.Errorf("%s's home must be an absolute path ending in /%s, not %q", u.Name, u.Name, u.Home)
		}
		if p := filepath.Dir(u.Home); p != parents[len(parents)-1] {
			parents = append(parents, p)
		}
	}
	for _, p := range parents {
		if err := os.MkdirAll(p, 0o711); err != nil {
			return nil, err
		}
		if err := searchable(p); err != nil {
			return nil, err
		}
	}
	iso := &Isolation{Helper: helper, Root: root, Users: users, free: make(chan User, len(users))}
	for _, u := range users {
		iso.free <- u
	}
	// The helper runs a command as a job user, or isolation does not work
	// here (no file capabilities, no_new_privs set on the server).
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, helper, append([]string{"run", users[0].Name, iso.Dir("probe")}, "true")...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s does not run commands as job users: %v %s", helper, err, strings.TrimSpace(out.String()))
	}
	return iso, nil
}

// notReplaceable makes sure no job user can rename d or a directory above
// it and put its own in its place: none is writable by others (or it is
// sticky, as /tmp). Ours are fixed; a Kubernetes emptyDir (0777, root's)
// cannot be.
func notReplaceable(d string) error {
	for p := d; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || os.Chmod(p, fi.Mode().Perm()&^0o022) != nil {
				return fmt.Errorf("a job could replace %s: %s is writable by others", d, p)
			}
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// searchable makes d and the directories above it searchable by others
// where they are ours (the image's home, a Compose volume made before), or
// says they are not; d itself is not listable by others.
func searchable(d string) error {
	if fi, err := os.Stat(d); err == nil && fi.Mode().Perm()&0o022 == 0 {
		if err := os.Chmod(d, fi.Mode().Perm()&0o700|0o011); err != nil {
			return err
		}
	}
	for p := filepath.Dir(d); ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o001 == 0 {
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || os.Chmod(p, fi.Mode().Perm()|0o001) != nil {
				return fmt.Errorf("job users cannot reach %s: %s is not searchable by others", d, p)
			}
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// Dir is a new directory for a job, a random suffix after ident.
func (i *Isolation) Dir(ident string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return filepath.Join(i.Root, ident+"-"+hex.EncodeToString(b))
}

// Acquire takes a free job user, waiting for one.
func (i *Isolation) Acquire(ctx context.Context) (User, error) {
	select {
	case u := <-i.free:
		return u, nil
	case <-ctx.Done():
		return User{}, ctx.Err()
	}
}

// Release gives a job user back.
func (i *Isolation) Release(u User) { i.free <- u }

// Command is args run by the helper as u in dir.
func (i *Isolation) Command(u User, dir string, args []string) []string {
	return append([]string{i.Helper, "run", u.Name, dir}, args...)
}

// Env is env with the job user's home, and the job's temporary directory
// (made by the helper in dir).
func Env(env []string, u User, dir string) []string {
	out := []string{}
	for _, e := range env {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "TMPDIR=") {
			out = append(out, e)
		}
	}
	return append(out, "HOME="+u.Home, "TMPDIR="+filepath.Join(dir, "tmp"))
}

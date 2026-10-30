package jobexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	prSetChildSubreaper = 36
	prSetNoNewPrivs     = 38
)

var dirName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CheckDir validates the directory a job is given: an absolute, clean path
// that does not exist yet, in a directory owned by uid that nobody else can
// write to, so no job user can have put anything there.
func CheckDir(dir string, uid int) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !dirName.MatchString(filepath.Base(dir)) {
		return fmt.Errorf("%q is not a clean absolute path", dir)
	}
	parent := filepath.Dir(dir)
	fi, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok {
		return fmt.Errorf("%s is not a directory", parent)
	}
	if int(st.Uid) != uid || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must be owned by uid %d and writable by it only", parent, uid)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s exists already", dir)
	}
	return nil
}

// noNewPrivs sets no_new_privs on every thread, so the job's processes
// inherit it.
func noNewPrivs() error {
	_, _, e := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0)
	if e == syscall.ENOTSUP {
		// Built with cgo: this thread only, the one that starts the job.
		_, _, e = syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0)
	}
	if e != 0 {
		return e
	}
	return nil
}

// Run runs args as the job user named name in dir (created, then
// removed), and returns the command's exit code.
func Run(passwd, name, dir string, args []string) (int, error) {
	runtime.LockOSThread()
	if len(args) == 0 {
		return 0, errors.New("no command")
	}
	u, err := Lookup(passwd, name)
	if err != nil {
		return 0, err
	}
	if u.UID == os.Getuid() {
		return 0, errors.New("the job user is the caller")
	}
	if err := CheckDir(dir, os.Getuid()); err != nil {
		return 0, err
	}
	if err := noNewPrivs(); err != nil {
		return 0, fmt.Errorf("no_new_privs: %w", err)
	}
	if err := killUser(u.UID); err != nil {
		return 0, err
	}
	if err := prepareHome(u); err != nil {
		return 0, err
	}
	defer emptyAs(u, u.Home)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return 0, err
	}
	defer removeAs(u, dir)
	// Its temporary directory, then the directory itself: once it is the
	// job user's, the caller cannot create anything in it.
	for _, d := range []string{filepath.Join(dir, "tmp"), dir} {
		if d != dir {
			if err := os.Mkdir(d, 0o700); err != nil {
				return 0, err
			}
		}
		if err := chown(d, u); err != nil {
			return 0, fmt.Errorf("giving %s to %s: %w", d, u.Name, err)
		}
	}
	// Files the job makes are its own only.
	syscall.Umask(0o077)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(u.UID), Gid: uint32(u.GID), Groups: []uint32{}},
		Setpgid:    true,
		Pdeathsig:  syscall.SIGKILL,
	}
	// Processes the job leaves behind are the helper's to reap, not PID 1's
	// (the server, in the image).
	syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s as %s: %w", args[0], u.Name, err)
	}
	go func() {
		<-sigs
		killUser(u.UID)
	}()
	err = cmd.Wait()
	killUser(u.UID)
	reap()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}

// prepareHome makes u's home an empty directory of u's, 0700, in a
// directory only the caller can write to.
func prepareHome(u User) error {
	parent := filepath.Dir(u.Home)
	fi, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must be a directory owned by uid %d and writable by it only", parent, os.Getuid())
	}
	if err := os.Mkdir(u.Home, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// Already the job user's (a slot used before): only the caller can
	// write to parent, so it is still the directory it made.
	hi, err := os.Lstat(u.Home)
	if err != nil {
		return err
	}
	if st, ok := hi.Sys().(*syscall.Stat_t); !hi.IsDir() || !ok {
		return fmt.Errorf("%s is not a directory", u.Home)
	} else if int(st.Uid) != u.UID {
		if err := chown(u.Home, u); err != nil {
			return fmt.Errorf("giving %s to %s: %w", u.Home, u.Name, err)
		}
	}
	// 0700 as its owner, the job user (the caller no longer may).
	emptyAs(u, u.Home)
	return nil
}

// reap waits for the helper's remaining children (orphans of the job,
// killed), until there are none.
func reap() {
	for {
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(-1, &ws, 0, nil); err != nil && err != syscall.EINTR {
			return
		}
	}
}

// chown gives the directory d (not a link) to u.
func chown(d string, u User) error {
	fd, err := syscall.Open(d, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return syscall.Fchown(fd, u.UID, u.GID)
}

// killUser kills every process of uid, until none is left (they may fork
// while being killed).
func killUser(uid int) error {
	for range 100 {
		pids, err := processesOf(uid)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("processes of uid %d are still running", uid)
}

// processesOf lists the processes whose real, effective or saved uid is uid
// (zombies excluded: they are already dead).
func processesOf(uid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	want := strconv.Itoa(uid)
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		var uids []string
		zombie := false
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 4 && f[0] == "Uid:" {
				uids = f[1:4]
			} else if len(f) >= 2 && f[0] == "State:" && f[1] == "Z" {
				zombie = true
			}
		}
		for _, u := range uids {
			if u == want && !zombie {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}

// removeAs removes dir: its content as the job user (emptyAs), then dir
// itself.
func removeAs(u User, dir string) {
	emptyAs(u, dir)
	os.Remove(dir)
}

// emptyAs removes what is in dir with the job user's file system identity
// (the job may have made its files unreadable to their owner).
func emptyAs(u User, dir string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gid, uid := os.Getgid(), os.Getuid()
	if setfs(syscall.SYS_SETFSGID, u.GID) && setfs(syscall.SYS_SETFSUID, u.UID) {
		os.Chmod(dir, 0o700)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			forceRemove(filepath.Join(dir, e.Name()))
		}
	}
	setfs(syscall.SYS_SETFSUID, uid)
	setfs(syscall.SYS_SETFSGID, gid)
}

// setfs sets this thread's file system uid or gid, and reports whether it
// took (the calls return the previous value, not an error).
func setfs(call uintptr, id int) bool {
	syscall.RawSyscall(call, uintptr(id), 0, 0)
	cur, _, _ := syscall.RawSyscall(call, ^uintptr(0), 0, 0)
	return int(cur) == id
}

func forceRemove(p string) {
	fi, err := os.Lstat(p)
	if err != nil {
		return
	}
	if fi.IsDir() {
		os.Chmod(p, 0o700)
		entries, _ := os.ReadDir(p)
		for _, e := range entries {
			forceRemove(filepath.Join(p, e.Name()))
		}
	}
	os.Remove(p)
}

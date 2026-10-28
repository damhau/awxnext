// Package runnercmd builds the command that runs `ansible-runner worker`
// locally, in a podman/docker execution environment container, or directly
// (when already inside an execution environment). It is shared by control
// nodes and remote agents.
package runnercmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Spec describes how to start a worker.
type Spec struct {
	// Runtime is "none" (run ansible-runner directly), "podman" or "docker".
	Runtime string
	// RunnerCommand is the ansible-runner binary for runtime "none".
	RunnerCommand string
	// WorkDir is the private data dir for runtime "none".
	WorkDir string
	// Image and Pull policy (always, missing, never) for container runtimes.
	Image, Pull string
	// Registry credentials for pulling Image.
	RegistryHost, RegistryUser, RegistryPassword string
	// Name identifies the container (used to remove it on cancel).
	Name string
	// KeepaliveSeconds makes the worker emit a keepalive every N idle
	// seconds (AWX_RUNNER_KEEPALIVE_SECONDS), so quiet tasks on remote nodes
	// and container groups are not cut off by idle timeouts.
	KeepaliveSeconds int
	// ShowPaths are host paths mounted into the execution environment
	// container (AWX_ISOLATION_SHOW_PATHS); see Mount.
	ShowPaths []string
}

// Mount parses an AWX_ISOLATION_SHOW_PATHS entry, HOST-DIR[:CONTAINER-DIR[:OPTIONS]],
// into the host path, the container path (the host path by default) and the
// mount options (for example ro, or z for SELinux).
func Mount(p string) (src, dst, opts string, err error) {
	parts := strings.SplitN(p, ":", 3)
	src, dst = parts[0], parts[0]
	if len(parts) > 1 && parts[1] != "" {
		dst = parts[1]
	}
	if len(parts) > 2 {
		opts = parts[2]
	}
	if !filepath.IsAbs(src) || !filepath.IsAbs(dst) {
		return "", "", "", fmt.Errorf("%q: paths must be absolute (HOST-DIR[:CONTAINER-DIR[:OPTIONS]])", p)
	}
	return filepath.Clean(src), filepath.Clean(dst), opts, nil
}

// workerArgs are the ansible-runner worker arguments after the binary.
func (s Spec) workerArgs(dir string) []string {
	args := []string{"worker", "--private-data-dir=" + dir}
	if s.KeepaliveSeconds > 0 {
		args = append(args, "--keepalive-seconds="+strconv.Itoa(s.KeepaliveSeconds))
	}
	return args
}

// Command returns the worker command line and a cleanup function.
func Command(s Spec) ([]string, func(), error) {
	noop := func() {}
	switch s.Runtime {
	case "none", "process", "":
		bin := s.RunnerCommand
		if bin == "" {
			bin = "ansible-runner"
		}
		if s.WorkDir == "" {
			return nil, noop, errors.New("no private data dir")
		}
		return append([]string{bin}, s.workerArgs(s.WorkDir)...), noop, nil
	case "podman", "docker":
		if s.Image == "" {
			return nil, noop, errors.New("no execution environment image")
		}
		args := []string{s.Runtime}
		var runArgs []string
		cleanup := noop
		if s.RegistryUser != "" {
			dir, err := os.MkdirTemp("", "awxnext-auth-")
			if err != nil {
				return nil, noop, err
			}
			cleanup = func() { os.RemoveAll(dir) }
			auth := base64.StdEncoding.EncodeToString([]byte(s.RegistryUser + ":" + s.RegistryPassword))
			cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{s.RegistryHost: map[string]string{"auth": auth}}})
			if s.Runtime == "docker" {
				// docker reads registry auth from <config dir>/config.json.
				if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
					cleanup()
					return nil, noop, err
				}
				args = append(args, "--config", dir)
			} else {
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), cfg, 0o600); err != nil {
					cleanup()
					return nil, noop, err
				}
				runArgs = append(runArgs, "--authfile", filepath.Join(dir, "auth.json"))
			}
		}
		args = append(args, "run")
		args = append(args, runArgs...)
		pull := s.Pull
		if pull == "" {
			pull = "missing"
		}
		args = append(args, "--rm", "-i", "--pull="+pull, "--name", s.Name, "--env", "HOME=/runner")
		if s.Runtime == "podman" {
			args = append(args, "--userns=keep-id")
		}
		for _, p := range s.ShowPaths {
			src, dst, opts, err := Mount(p)
			if err != nil {
				cleanup()
				return nil, noop, err
			}
			v := src + ":" + dst
			if opts != "" {
				v += ":" + opts
			}
			args = append(args, "-v", v)
		}
		args = append(args, s.Image, "ansible-runner")
		args = append(args, s.workerArgs("/runner")...)
		return args, cleanup, nil
	}
	return nil, noop, fmt.Errorf("unknown container runtime %q", s.Runtime)
}

// Kill stops a worker started from s.
func Kill(s Spec, cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
	if s.Runtime == "podman" || s.Runtime == "docker" {
		exec.Command(s.Runtime, "rm", "-f", s.Name).Run()
	}
}

// Env is the environment passed to worker commands.
func Env() []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8"}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "XDG_RUNTIME_DIR", "CONTAINER_HOST", "DOCKER_HOST", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

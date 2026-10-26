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
		return []string{bin, "worker", "--private-data-dir", s.WorkDir}, noop, nil
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
		args = append(args, s.Image, "ansible-runner", "worker", "--private-data-dir=/runner")
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

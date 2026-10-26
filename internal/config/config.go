// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL string
	Listen      string
	SecretKey   string
	// BaseURL is the externally reachable URL (used for OIDC redirects, callbacks, notifications).
	BaseURL string
	// NodeName identifies this control node (instance hostname).
	NodeName string
	// ProjectsRoot is where project checkouts live.
	ProjectsRoot string
	// JobsRoot is where per-job private data dirs are created.
	JobsRoot string
	// ContainerRuntime: "podman", "docker", "kubernetes" or "none" (run ansible-runner directly).
	ContainerRuntime string
	// DefaultEEImage is used when no execution environment is selected.
	DefaultEEImage string
	// RunnerCommand is the ansible-runner binary used when ContainerRuntime is "none".
	RunnerCommand string
	// UIDir serves a built UI from disk when set; otherwise the embedded UI is used.
	UIDir string
	// Capacity is the forks capacity of this node for local execution.
	Capacity int
	DevMode  bool
	// InternalURL is how other control nodes, agents and job pods reach this node.
	InternalURL string
	// MetricsToken protects /metrics with a bearer token when set.
	MetricsToken string
	// LogFormat is "text" or "json"; LogLevel is debug, info, warn or error.
	LogFormat, LogLevel string
	// ShimImage is the image whose /awxnext binary is copied into job pods (container groups).
	ShimImage string
	// ServiceURL is how job pods reach the control plane (defaults to BaseURL).
	ServiceURL string
}

func Load() *Config {
	host, _ := os.Hostname()
	c := &Config{
		DatabaseURL:      env("AWXNEXT_DATABASE_URL", "postgres://postgres@localhost:5432/awxnext?sslmode=disable"),
		Listen:           env("AWXNEXT_LISTEN", ":8052"),
		SecretKey:        env("AWXNEXT_SECRET_KEY", ""),
		BaseURL:          strings.TrimRight(env("AWXNEXT_BASE_URL", "http://localhost:8052"), "/"),
		NodeName:         env("AWXNEXT_NODE_NAME", host),
		ProjectsRoot:     env("AWXNEXT_PROJECTS_ROOT", "/var/lib/awxnext/projects"),
		JobsRoot:         env("AWXNEXT_JOBS_ROOT", "/var/lib/awxnext/jobs"),
		ContainerRuntime: env("AWXNEXT_CONTAINER_RUNTIME", "podman"),
		DefaultEEImage:   env("AWXNEXT_DEFAULT_EE_IMAGE", "quay.io/ansible/awx-ee:latest"),
		RunnerCommand:    env("AWXNEXT_RUNNER_CMD", "ansible-runner"),
		UIDir:            env("AWXNEXT_UI_DIR", ""),
		Capacity:         envInt("AWXNEXT_CAPACITY", 100),
		DevMode:          env("AWXNEXT_DEV", "") != "",
		InternalURL:      strings.TrimRight(env("AWXNEXT_INTERNAL_URL", ""), "/"),
		MetricsToken:     env("AWXNEXT_METRICS_TOKEN", ""),
		LogFormat:        env("AWXNEXT_LOG_FORMAT", "text"),
		LogLevel:         env("AWXNEXT_LOG_LEVEL", "info"),
		ShimImage:        env("AWXNEXT_SHIM_IMAGE", "ghcr.io/damhau/awxnext:latest"),
	}
	c.ServiceURL = strings.TrimRight(env("AWXNEXT_SERVICE_URL", c.BaseURL), "/")
	return c
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

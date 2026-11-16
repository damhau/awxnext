// Package connectors holds what runs an event connector where jobs run: the
// runner, a Python script awxnext sends on the process's stdin at each
// start, and the command that starts it in an image or on a node.
//
// The process reads JSON objects, one a line, until its stdin is closed, and
// merges them: first the site (where awxnext is, as the node or pod that
// starts it reaches it), then the run's configuration from the controller,
// which carries the runner's source.
package connectors

import (
	_ "embed"
	"encoding/json"
	"os"
	"strings"

	"github.com/damhau/awxnext/internal/runnercmd"
)

//go:embed runner.py
var Runner string

// Exit codes of the runner.
const (
	ExitPlugin  = 1
	ExitConfig  = 2
	ExitRefused = 3
)

// boot reads the configuration and runs the runner in it. stderr joins
// stdout, the log awxnext streams.
const boot = `import json,os,sys
os.dup2(1,2)
c={}
for l in sys.stdin:
    l=l.strip()
    if l:c.update(json.loads(l))
exec(compile(c.pop("runner"),"awxnext-connector-runner","exec"),{"__name__":"__main__","CONFIG":c})`

// pick starts boot in the Python given, else in ansible-rulebook's (a
// decision environment's plugin dependencies are installed for it), else in
// python3.
const pick = `p=$1
if [ -z "$p" ] && r=$(command -v ansible-rulebook); then
	p=$(sed -n '1s/^#! *//p' "$r")
	case "$p" in *python*) ;; *) p= ;; esac
fi
exec ${p:-python3} -u -c "$0"`

// Argv is the command that runs a connector, python being its interpreter
// or empty.
func Argv(python string) []string {
	return []string{"sh", "-c", pick, boot, python}
}

// Site is where the runner posts: awxnext's URL as the place it runs in
// reaches it, and how to trust its certificate.
type Site struct {
	BaseURL  string `json:"base_url"`
	CA       string `json:"ca,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// Line is the site as the runner reads it.
func (s Site) Line() []byte {
	b, _ := json.Marshal(s)
	return append(b, '\n')
}

// SiteFor is the site of a node that reaches awxnext at url, trusting the
// PEM file caFile, if any.
func SiteFor(url, caFile string, insecure bool) (Site, error) {
	s := Site{BaseURL: BaseURL(url), Insecure: insecure}
	if caFile != "" {
		b, err := os.ReadFile(caFile)
		if err != nil {
			return s, err
		}
		s.CA = string(b)
	}
	return s, nil
}

// BaseURL is awxnext's URL from a work unit's or a control plane's URL.
func BaseURL(url string) string {
	if i := strings.Index(url, "/agent/v1/"); i >= 0 {
		url = url[:i]
	}
	switch {
	case strings.HasPrefix(url, "wss://"):
		url = "https://" + url[len("wss://"):]
	case strings.HasPrefix(url, "ws://"):
		url = "http://" + url[len("ws://"):]
	}
	return strings.TrimRight(url, "/")
}

// File is a credential's file: the runner writes it where only it can read
// it, and puts its path in place of Placeholder in the arguments and the
// environment.
type File struct {
	Placeholder string `json:"placeholder"`
	Content     string `json:"content"`
}

// Config is a run's configuration, as the controller sends it.
type Config struct {
	Runner    string           `json:"runner"`
	Connector int64            `json:"connector"`
	Name      string           `json:"name"`
	PostPath  string           `json:"post_path"`
	Token     string           `json:"token"`
	Source    string           `json:"source"`
	Args      map[string]any   `json:"args"`
	Filters   []map[string]any `json:"filters"`
	Env       map[string]any   `json:"env,omitempty"`
	Files     []File           `json:"files,omitempty"`
	Buffer    int              `json:"buffer_size"`
	Batch     int              `json:"batch_size"`
}

// Line is the configuration as the runner reads it.
func (c Config) Line() ([]byte, error) {
	b, err := json.Marshal(c)
	return append(b, '\n'), err
}

// Status is what the runner last reported of its posting.
type Status struct {
	State       string `json:"state"`
	Sent        int64  `json:"sent"`
	Buffered    int64  `json:"buffered"`
	Dropped     int64  `json:"dropped"`
	Rejected    int64  `json:"rejected"`
	Retries     int64  `json:"retries"`
	Unreachable bool   `json:"unreachable"`
	LastError   string `json:"last_error"`
}

const statusPrefix = "awxnext-status "

// ParseStatus reads a status line of the runner's output.
func ParseStatus(line string) (Status, bool) {
	var s Status
	rest, ok := strings.CutPrefix(line, statusPrefix)
	if !ok || json.Unmarshal([]byte(rest), &s) != nil {
		return s, false
	}
	return s, true
}

// Env is the environment of a connector on a node: a job's, and where
// Ansible collections are.
func Env() []string {
	env := runnercmd.Env()
	for _, k := range []string{"ANSIBLE_COLLECTIONS_PATH", "ANSIBLE_COLLECTIONS_PATHS"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/damhau/awxnext/internal/runnercmd"
)

// Config configures an agent (remote execution node) or a job pod shim.
type Config struct {
	// URL is the control plane base URL (https://awx.example.com).
	URL string
	// Token authenticates the agent (instance agent token) or shim (unit token).
	Token string
	// Runtime runs ansible-runner directly ("none") or in podman/docker execution environments.
	Runtime string
	// RunnerCommand is the ansible-runner binary for runtime "none".
	RunnerCommand string
	// WorkDir holds private data dirs for runtime "none".
	WorkDir string
	// Capacity overrides the computed capacity (forks).
	Capacity int
	// ExactWorkDir uses WorkDir itself as the private data dir (job pods use /runner).
	ExactWorkDir bool
	// CACert is a PEM file trusted for the control plane's TLS certificate.
	CACert string
	// InsecureSkipVerify disables TLS verification (testing only).
	InsecureSkipVerify bool
	Version            string
}

func (cfg *Config) httpClient() (*http.Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in for testing
	if cfg.CACert != "" {
		pem, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificates found in " + cfg.CACert)
		}
		tlsCfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	return &http.Client{Transport: tr}, nil
}

func (cfg *Config) dial(ctx context.Context, client *http.Client, u string) (*websocket.Conn, error) {
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}}})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(readLimit)
	return ws, nil
}

// RunAgent keeps a control connection to the control plane and runs the work it offers.
func RunAgent(ctx context.Context, cfg Config) error {
	if cfg.Runtime == "" {
		cfg.Runtime = "podman"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/var/lib/awxnext-agent"
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return err
	}
	client, err := cfg.httpClient()
	if err != nil {
		return err
	}
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := cfg.session(ctx, client)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("control connection lost, reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return nil
}

func (cfg *Config) session(ctx context.Context, client *http.Client) error {
	ws, err := cfg.dial(ctx, client, strings.TrimRight(cfg.URL, "/")+"/agent/v1/connect")
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	send := func(m message) error {
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		wctx, c := context.WithTimeout(ctx, 10*time.Second)
		defer c()
		return ws.Write(wctx, websocket.MessageText, b)
	}
	host, _ := os.Hostname()
	if err := send(message{Type: "register", Hostname: host, Version: cfg.Version, Runtime: cfg.Runtime, WorkDir: cfg.WorkDir,
		CPU: float64(runtime.NumCPU()), Memory: totalMemory(), Capacity: cfg.Capacity}); err != nil {
		return err
	}
	slog.Info("connected to control plane", "url", cfg.URL, "runtime", cfg.Runtime)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if send(message{Type: "heartbeat"}) != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type == "work" {
			path := m.Path
			go func() {
				if err := cfg.runUnit(context.Background(), client, strings.TrimRight(cfg.URL, "/")+path); err != nil {
					slog.Error("work unit failed", "unit", m.Unit, "err", err)
				}
			}()
		}
	}
}

// RunShim runs a single work unit (inside a job pod).
func RunShim(ctx context.Context, cfg Config, url string) error {
	if cfg.Runtime == "" {
		cfg.Runtime = "none"
	}
	client, err := cfg.httpClient()
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if lastErr = cfg.runUnit(ctx, client, url); lastErr == nil || !errors.Is(lastErr, errDial) {
			return lastErr
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return lastErr
}

var errDial = errors.New("connecting to the control plane")

// runUnit runs one worker for a work unit.
func (cfg *Config) runUnit(ctx context.Context, client *http.Client, url string) error {
	ws, err := cfg.dial(ctx, client, url)
	if err != nil {
		return fmt.Errorf("%w: %v", errDial, err)
	}
	defer ws.CloseNow()
	var wmu sync.Mutex
	write := func(typ websocket.MessageType, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		wctx, c := context.WithTimeout(ctx, time.Minute)
		defer c()
		return ws.Write(wctx, typ, b)
	}
	done := func(rc int, msg string) error {
		b, _ := json.Marshal(message{Type: "done", RC: &rc, Error: msg})
		err := write(websocket.MessageText, b)
		ws.Close(websocket.StatusNormalClosure, "")
		return err
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	var first message
	if json.Unmarshal(data, &first) != nil || first.Type != "spec" || first.Spec == nil {
		return done(-1, "protocol error: expected work spec")
	}
	spec := *first.Spec
	if strings.ContainsAny(spec.Ident, "/\\") || spec.Ident == "" || spec.Ident == ".." {
		return done(-1, "invalid work ident")
	}
	rs := runnercmd.Spec{Runtime: cfg.Runtime, RunnerCommand: cfg.RunnerCommand, Image: spec.Image, Pull: spec.Pull,
		RegistryHost: spec.RegistryHost, RegistryUser: spec.RegistryUser, RegistryPassword: spec.RegistryPassword, Name: "awxnext_job_" + spec.Ident}
	switch {
	case (rs.Runtime == "none" || rs.Runtime == "") && cfg.ExactWorkDir:
		rs.WorkDir = cfg.WorkDir
		if err := os.MkdirAll(rs.WorkDir, 0o700); err != nil {
			return done(-1, err.Error())
		}
	case rs.Runtime == "none" || rs.Runtime == "":
		rs.WorkDir = filepath.Join(cfg.WorkDir, spec.Ident)
		os.RemoveAll(rs.WorkDir)
		if err := os.MkdirAll(rs.WorkDir, 0o700); err != nil {
			return done(-1, err.Error())
		}
		defer os.RemoveAll(rs.WorkDir)
	}
	args, cleanup, err := runnercmd.Command(rs)
	if err != nil {
		return done(-1, err.Error())
	}
	defer cleanup()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = runnercmd.Env()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return done(-1, fmt.Sprintf("starting %s: %v", args[0], err))
	}
	var killed atomic.Bool
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				// The control plane went away: stop the worker.
				runnercmd.Kill(rs, cmd)
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := stdin.Write(data); err != nil {
					continue
				}
				continue
			}
			var m message
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "eof":
				stdin.Close()
			case "cancel":
				killed.Store(true)
				runnercmd.Kill(rs, cmd)
			}
		}
	}()
	buf := make([]byte, 64<<10)
	rd := bufio.NewReaderSize(stdout, 64<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			if werr := write(websocket.MessageBinary, buf[:n]); werr != nil {
				runnercmd.Kill(rs, cmd)
				cmd.Wait()
				return werr
			}
		}
		if err != nil {
			break
		}
	}
	werr := cmd.Wait()
	rc := 0
	var msg string
	if werr != nil {
		rc = -1
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			rc = ee.ExitCode()
		}
		msg = strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = werr.Error()
		}
		if killed.Load() {
			msg = "canceled"
		}
	}
	return done(rc, msg)
}

// tailBuffer keeps the last 64 KiB written.
type tailBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b.Write(p)
	if t.b.Len() > 64<<10 {
		rest := t.b.Bytes()[t.b.Len()-(64<<10):]
		t.b.Reset()
		t.b.Write(rest)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}

func totalMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// InstallShim copies the running binary to dest (used by the job pod init container).
func InstallShim(dest string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

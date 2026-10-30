// Package agent runs ansible-runner workers away from the control node:
// on remote execution nodes (an agent dialing back over a websocket) and in
// Kubernetes job pods (a shim injected into the execution environment that
// dials back the same way). The control node streams the transmit payload
// to the worker and reads the worker's output stream back, exactly like a
// local `ansible-runner worker` process.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/capacity"
	"github.com/go-chi/chi/v5"
)

const notifyChannel = "awx_agent"

// readLimit bounds websocket messages (data chunks are 64 KiB).
const readLimit = 16 << 20

// WorkSpec tells the worker side how to run ansible-runner.
type WorkSpec struct {
	Ident            string   `json:"ident"`
	Image            string   `json:"image,omitempty"`
	Pull             string   `json:"pull,omitempty"`
	RegistryHost     string   `json:"registry_host,omitempty"`
	RegistryUser     string   `json:"registry_user,omitempty"`
	RegistryPassword string   `json:"registry_password,omitempty"`
	KeepaliveSeconds int      `json:"keepalive_seconds,omitempty"`
	ShowPaths        []string `json:"show_paths,omitempty"`
}

type message struct {
	Type string `json:"type"`
	// work offers
	Unit string `json:"unit,omitempty"`
	Path string `json:"path,omitempty"`
	// work stream
	Spec  *WorkSpec `json:"spec,omitempty"`
	RC    *int      `json:"rc,omitempty"`
	Error string    `json:"error,omitempty"`
	// registration
	Hostname string  `json:"hostname,omitempty"`
	Version  string  `json:"version,omitempty"`
	Runtime  string  `json:"runtime,omitempty"`
	WorkDir  string  `json:"work_dir,omitempty"`
	CPU      float64 `json:"cpu,omitempty"`
	Memory   int64   `json:"memory,omitempty"`
	Capacity int     `json:"capacity,omitempty"`
}

// Worker is a running remote ansible-runner worker.
type Worker interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Wait blocks until the worker ended and returns diagnostics (stderr tail).
	Wait() (string, error)
	Kill()
}

// Broker routes work between control nodes and agents.
type Broker struct {
	app    *api.App
	node   string
	mu     sync.Mutex
	agents map[string]*agentConn
	units  map[string]*unit
	// PickupTimeout bounds how long an agent may take to accept work.
	PickupTimeout time.Duration
}

// Default is the broker of this process (set by Install).
var Default *Broker

// Install registers the agent endpoints.
func Install(app *api.App) *Broker {
	b := &Broker{app: app, node: app.Cfg.NodeName, agents: map[string]*agentConn{}, units: map[string]*unit{}, PickupTimeout: time.Minute}
	app.HandlePublic("GET", "/agent/v1/connect", b.handleConnect)
	app.HandlePublic("GET", "/agent/v1/work/{controller}/{unit}", b.handleWork)
	Default = b
	return b
}

// HashToken returns the stored form of an agent token.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// NewToken returns a random agent or unit token.
func NewToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// agentFor authenticates an agent token.
func (b *Broker) agentFor(ctx context.Context, tok string) (string, error) {
	if tok == "" {
		return "", api.ErrUnauthorized
	}
	var host string
	err := b.app.DB.QueryRow(ctx, "SELECT hostname FROM instances WHERE agent_token_hash = $1 AND node_type = 'execution'", HashToken(tok)).Scan(&host)
	if err != nil {
		return "", api.ErrUnauthorized
	}
	return host, nil
}

type agentConn struct {
	host string
	ws   *websocket.Conn
	mu   sync.Mutex
}

func (a *agentConn) send(ctx context.Context, m message) error {
	b, _ := json.Marshal(m)
	a.mu.Lock()
	defer a.mu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.ws.Write(wctx, websocket.MessageText, b)
}

func (b *Broker) handleConnect(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	host, err := b.agentFor(r.Context(), bearer(r))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := &agentConn{host: host, ws: ws}
	b.mu.Lock()
	if old := b.agents[host]; old != nil {
		old.ws.Close(websocket.StatusPolicyViolation, "replaced by a new connection")
	}
	b.agents[host] = conn
	b.mu.Unlock()
	slog.Info("agent connected", "node", host)
	defer func() {
		b.mu.Lock()
		if b.agents[host] == conn {
			delete(b.agents, host)
			b.app.DB.Exec(context.Background(), "UPDATE instances SET node_state = 'unavailable' WHERE hostname = $1", host)
		}
		b.mu.Unlock()
		slog.Info("agent disconnected", "node", host)
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "register":
			// AWX's capacity of an execution node, from the resources it
			// reports; the agent's --capacity, when set, is both capacities.
			cpuCap, memCap := capacity.CPU(m.CPU, false), capacity.Memory(m.Memory, false)
			if m.Capacity > 0 {
				cpuCap, memCap = m.Capacity, m.Capacity
			}
			var forks int
			if err := b.app.DB.QueryRow(ctx, `UPDATE instances SET node_state = 'ready', last_seen = now(), last_health_check = now(), errors = '',
				version = $2, cpu = $3, memory = $4, cpu_capacity = $5, mem_capacity = $6, capacity = `+capacity.SQL("$5", "$6")+`, agent_runtime = $7, agent_work_dir = $8
				WHERE hostname = $1 RETURNING capacity`, host, m.Version, m.CPU, m.Memory, cpuCap, memCap, m.Runtime, m.WorkDir).Scan(&forks); err != nil {
				slog.Error("agent registration", "node", host, "err", err)
			}
			conn.send(ctx, message{Type: "registered", Capacity: forks})
			b.app.DB.Exec(ctx, "SELECT pg_notify('awx_dispatch', '')")
		case "heartbeat":
			b.app.DB.Exec(ctx, "UPDATE instances SET last_seen = now(), node_state = 'ready' WHERE hostname = $1", host)
		}
	}
}

// Start listens for work offers addressed to agents connected to this node.
func (b *Broker) Start(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			if err := b.listen(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("agent listener restarting", "err", err)
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

func (b *Broker) listen(ctx context.Context) error {
	pc, err := b.app.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	conn := pc.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var offer struct {
			Node string  `json:"node"`
			Msg  message `json:"msg"`
		}
		if json.Unmarshal([]byte(n.Payload), &offer) != nil {
			continue
		}
		b.mu.Lock()
		a := b.agents[offer.Node]
		b.mu.Unlock()
		if a != nil {
			a.send(ctx, offer.Msg)
		}
	}
}

// RequestHealthCheck asks the agent of an execution node to report its
// resources again, directly or through the control node it is connected
// to; its answer updates the instance as its registration does.
func (b *Broker) RequestHealthCheck(ctx context.Context, node string) error {
	m := message{Type: "health_check"}
	b.mu.Lock()
	a := b.agents[node]
	b.mu.Unlock()
	if a != nil {
		return a.send(ctx, m)
	}
	payload, _ := json.Marshal(map[string]any{"node": node, "msg": m})
	_, err := b.app.DB.Exec(ctx, "SELECT pg_notify($1, $2)", notifyChannel, string(payload))
	return err
}

// StartWorker offers a work unit to the agent of an execution node and waits for it to connect.
func (b *Broker) StartWorker(ctx context.Context, node string, spec WorkSpec) (Worker, error) {
	u := b.newUnit(node, "", spec)
	offer := message{Type: "work", Unit: u.id, Path: u.path(b.node)}
	b.mu.Lock()
	a := b.agents[node]
	b.mu.Unlock()
	var err error
	if a != nil {
		err = a.send(ctx, offer)
	} else {
		payload, _ := json.Marshal(map[string]any{"node": node, "msg": offer})
		_, err = b.app.DB.Exec(ctx, "SELECT pg_notify($1, $2)", notifyChannel, string(payload))
	}
	if err != nil {
		b.drop(u)
		return nil, fmt.Errorf("offering work to %s: %w", node, err)
	}
	select {
	case <-u.connected:
		return u, nil
	case <-time.After(b.PickupTimeout):
		b.drop(u)
		return nil, fmt.Errorf("the agent of execution node %s did not pick up the job within %s", node, b.PickupTimeout)
	case <-ctx.Done():
		b.drop(u)
		return nil, ctx.Err()
	}
}

func (b *Broker) newUnit(node, token string, spec WorkSpec) *unit {
	u := &unit{id: NewToken()[:24], node: node, token: token, spec: spec, broker: b,
		connected: make(chan struct{}), done: make(chan struct{}), cancel: make(chan struct{})}
	u.stdinR, u.stdinW = io.Pipe()
	u.stdoutR, u.stdoutW = io.Pipe()
	b.mu.Lock()
	b.units[u.id] = u
	b.mu.Unlock()
	return u
}

func (b *Broker) drop(u *unit) {
	b.mu.Lock()
	delete(b.units, u.id)
	b.mu.Unlock()
	u.stdinR.CloseWithError(errors.New("work unit closed"))
	u.stdoutW.CloseWithError(errors.New("work unit closed"))
}

func (b *Broker) handleWork(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	controller, id := chi.URLParam(r, "controller"), chi.URLParam(r, "unit")
	if controller != b.node {
		b.proxy(w, r, controller)
		return
	}
	b.mu.Lock()
	u := b.units[id]
	b.mu.Unlock()
	if u == nil {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	tok := bearer(r)
	switch {
	case u.token != "":
		if subtle.ConstantTimeCompare([]byte(tok), []byte(u.token)) != 1 {
			api.WriteError(w, api.ErrUnauthorized)
			return
		}
	default:
		host, err := b.agentFor(r.Context(), tok)
		if err != nil || host != u.node {
			api.WriteError(w, api.ErrUnauthorized)
			return
		}
	}
	if !u.claim() {
		api.WriteError(w, api.Detail(409, "Work unit already connected."))
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		u.finish(-1, "", err)
		return
	}
	ws.SetReadLimit(readLimit)
	u.stream(r.Context(), ws)
}

// proxy forwards a work connection to the control node owning the unit.
func (b *Broker) proxy(w http.ResponseWriter, r *http.Request, controller string) {
	var target string
	b.app.DB.QueryRow(r.Context(), "SELECT internal_url FROM instances WHERE hostname = $1", controller).Scan(&target)
	u, err := url.Parse(target)
	if target == "" || err != nil {
		api.WriteError(w, api.Detail(502, "Control node "+controller+" is not reachable from this node (set AWXNEXT_INTERNAL_URL)."))
		return
	}
	if r.Header.Get("X-Awxnext-Proxied") != "" {
		api.WriteError(w, api.Detail(508, "Proxy loop."))
		return
	}
	r.Header.Set("X-Awxnext-Proxied", b.node)
	httputil.NewSingleHostReverseProxy(u).ServeHTTP(w, r)
}

// unit is one worker run, controller side.
type unit struct {
	id, node, token string
	spec            WorkSpec
	broker          *Broker
	stdinR          *io.PipeReader
	stdinW          *io.PipeWriter
	stdoutR         *io.PipeReader
	stdoutW         *io.PipeWriter
	claimed         sync.Once
	connected       chan struct{}
	finishOnce      sync.Once
	done            chan struct{}
	diag            string
	err             error
	cancelOnce      sync.Once
	cancel          chan struct{}
}

func (u *unit) path(controller string) string {
	return "/agent/v1/work/" + controller + "/" + u.id
}

func (u *unit) claim() bool {
	ok := false
	u.claimed.Do(func() {
		ok = true
		close(u.connected)
	})
	return ok
}

func (u *unit) Stdin() io.WriteCloser { return u.stdinW }
func (u *unit) Stdout() io.Reader     { return u.stdoutR }

func (u *unit) Wait() (string, error) {
	<-u.done
	u.broker.drop(u)
	return u.diag, u.err
}

// Kill asks the worker side to stop; the unit is closed if it does not answer.
func (u *unit) Kill() {
	u.cancelOnce.Do(func() {
		close(u.cancel)
		go func() {
			select {
			case <-u.done:
			case <-time.After(30 * time.Second):
				u.finish(-1, "", errors.New("worker did not acknowledge cancellation"))
			}
		}()
	})
}

func (u *unit) finish(rc int, diag string, err error) {
	u.finishOnce.Do(func() {
		u.diag, u.err = diag, err
		u.stdoutW.Close()
		u.stdinR.CloseWithError(errors.New("worker finished"))
		close(u.done)
	})
}

// stream pumps the transmit data to the worker and its output back.
func (u *unit) stream(ctx context.Context, ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(typ websocket.MessageType, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		wctx, c := context.WithTimeout(ctx, time.Minute)
		defer c()
		return ws.Write(wctx, typ, b)
	}
	text := func(m message) error {
		b, _ := json.Marshal(m)
		return write(websocket.MessageText, b)
	}
	spec := u.spec
	if err := text(message{Type: "spec", Spec: &spec}); err != nil {
		u.finish(-1, "", err)
		return
	}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := u.stdinR.Read(buf)
			if n > 0 {
				if werr := write(websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					text(message{Type: "eof"})
				}
				return
			}
		}
	}()
	go func() {
		select {
		case <-u.cancel:
			text(message{Type: "cancel"})
		case <-ctx.Done():
		}
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			u.finish(-1, "", fmt.Errorf("lost connection to the worker: %w", err))
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := u.stdoutW.Write(data); err != nil {
				// Nobody reads the output any more; keep draining until done.
				continue
			}
			continue
		}
		var m message
		if json.Unmarshal(data, &m) != nil || m.Type != "done" {
			continue
		}
		rc := -1
		if m.RC != nil {
			rc = *m.RC
		}
		var ferr error
		if rc < 0 && m.Error != "" {
			ferr = errors.New(m.Error)
		}
		u.finish(rc, m.Error, ferr)
		ws.Close(websocket.StatusNormalClosure, "")
		return
	}
}

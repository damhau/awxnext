// Package logship is AWX's external logging: the logs of AWX's loggers
// (awx, and awx.analytics.activity_stream, job_events and system_tracking)
// formatted as AWX's LogstashFormatter, and sent where AWX's rsyslogd
// sends them: an HTTP(S) POST per message (omhttp), or a TCP line or a UDP
// datagram (omfwd), as the LOG_AGGREGATOR_* settings say.
package logship

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default is the server's shipper; nil until the server starts one.
var Default *Shipper

// Emit sends a message of one of AWX's loggers through the default
// shipper, when external logging takes it.
func Emit(ctx context.Context, logger string, level slog.Level, msg string, fields map[string]any) {
	if Default != nil {
		Default.Log(ctx, logger, level, msg, fields)
	}
}

// Shipper queues messages and sends them to the log aggregator.
type Shipper struct {
	get     func(ctx context.Context, key string) any
	decrypt func(string) (string, error)
	node    string
	host    string
	// diag is the server's own log, not shipped.
	diag *slog.Logger

	mu    sync.Mutex
	wake  chan struct{}
	queue []entry

	conn    net.Conn
	connKey string
	clients [2]*http.Client
}

// client is the HTTP client verifying certificates or not
// (LOG_AGGREGATOR_VERIFY_CERT), used only by Run.
func (s *Shipper) client(verify bool) *http.Client {
	i := 0
	if verify {
		i = 1
	}
	if s.clients[i] == nil {
		s.clients[i] = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{InsecureSkipVerify: !verify}}}
	}
	return s.clients[i]
}

type entry struct {
	data  []byte
	level slog.Level
}

// New returns a shipper reading the settings with get; node is
// CLUSTER_HOST_ID and diag the handler of the server's own log.
func New(get func(ctx context.Context, key string) any, decrypt func(string) (string, error), node string, diag slog.Handler) *Shipper {
	host, _ := os.Hostname()
	return &Shipper{get: get, decrypt: decrypt, node: node, host: host, diag: slog.New(diag), wake: make(chan struct{}, 1)}
}

// config is the LOG_AGGREGATOR_* settings.
type config struct {
	enabled              bool
	host                 string
	port                 int
	kind, protocol       string
	username, password   string
	loggers              []string
	level                slog.Level
	timeout              time.Duration
	verify, debug, facts bool
	queueSize            int
	uuid                 string
}

func (s *Shipper) str(ctx context.Context, key string) string {
	v, _ := s.get(ctx, key).(string)
	return v
}

func (s *Shipper) num(ctx context.Context, key string) int {
	n, _ := strconv.Atoi(fmt.Sprint(s.get(ctx, key)))
	return n
}

func (s *Shipper) flag(ctx context.Context, key string) bool {
	b, _ := s.get(ctx, key).(bool)
	return b
}

func (s *Shipper) config(ctx context.Context) config {
	c := config{enabled: s.flag(ctx, "LOG_AGGREGATOR_ENABLED")}
	if !c.enabled {
		return c
	}
	c.host, c.port = s.str(ctx, "LOG_AGGREGATOR_HOST"), s.num(ctx, "LOG_AGGREGATOR_PORT")
	c.kind, c.protocol = s.str(ctx, "LOG_AGGREGATOR_TYPE"), s.str(ctx, "LOG_AGGREGATOR_PROTOCOL")
	c.username = s.str(ctx, "LOG_AGGREGATOR_USERNAME")
	if pw := s.str(ctx, "LOG_AGGREGATOR_PASSWORD"); pw != "" {
		c.password, _ = s.decrypt(pw)
	}
	if l, ok := s.get(ctx, "LOG_AGGREGATOR_LOGGERS").([]any); ok {
		for _, x := range l {
			c.loggers = append(c.loggers, fmt.Sprint(x))
		}
	}
	var ok bool
	if c.level, ok = levels[s.str(ctx, "LOG_AGGREGATOR_LEVEL")]; !ok {
		c.level = slog.LevelWarn
	}
	c.timeout = time.Duration(max(s.num(ctx, "LOG_AGGREGATOR_TCP_TIMEOUT"), 1)) * time.Second
	c.verify = s.flag(ctx, "LOG_AGGREGATOR_VERIFY_CERT")
	c.debug = s.flag(ctx, "LOG_AGGREGATOR_RSYSLOGD_DEBUG")
	c.facts = s.flag(ctx, "LOG_AGGREGATOR_INDIVIDUAL_FACTS")
	c.queueSize = max(s.num(ctx, "LOG_AGGREGATOR_ACTION_QUEUE_SIZE"), 1)
	if c.uuid = s.str(ctx, "LOG_AGGREGATOR_TOWER_UUID"); c.uuid == "" {
		c.uuid = s.str(ctx, "INSTALL_UUID")
	}
	return c
}

// AWX's level names; CRITICAL is above slog's ERROR.
var levels = map[string]slog.Level{"DEBUG": slog.LevelDebug, "INFO": slog.LevelInfo, "WARNING": slog.LevelWarn, "ERROR": slog.LevelError, "CRITICAL": slog.LevelError + 4}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError+4:
		return "CRITICAL"
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}

// takes is AWX's ExternalLoggerEnabled and DynamicLevelFilter: the
// logger's headline (awx.analytics.<name>) or base name (awx.<...>) is
// in LOG_AGGREGATOR_LOGGERS, and the level reaches LOG_AGGREGATOR_LEVEL.
func (c config) takes(logger string, level slog.Level) bool {
	if !c.enabled || level < c.level {
		return false
	}
	name, _, _ := strings.Cut(logger, ".")
	if rest, ok := strings.CutPrefix(logger, "awx.analytics."); ok {
		name = rest[strings.LastIndex(rest, ".")+1:]
	}
	for _, l := range c.loggers {
		if l == name {
			return true
		}
	}
	return false
}

// Takes reports whether external logging takes messages of a logger at a
// level, to skip building them.
func Takes(ctx context.Context, logger string, level slog.Level) bool {
	return Default != nil && Default.config(ctx).takes(logger, level)
}

// IndividualFacts reports whether system tracking facts go one by one
// (LOG_AGGREGATOR_INDIVIDUAL_FACTS); false when nothing takes them.
func IndividualFacts(ctx context.Context) bool {
	if Default == nil {
		return false
	}
	c := Default.config(ctx)
	return c.facts && c.takes("awx.analytics.system_tracking", slog.LevelInfo)
}

// Log formats and queues a message, if external logging takes it.
func (s *Shipper) Log(ctx context.Context, logger string, level slog.Level, msg string, fields map[string]any) {
	c := s.config(ctx)
	if !c.takes(logger, level) {
		return
	}
	s.enqueue(c, entry{data: s.format(c, time.Now(), logger, level, msg, fields), level: level})
}

// format is AWX's LogstashFormatter: the message, its logger, level and
// extra fields, cluster_host_id and tower_uuid; under Splunk's "event",
// with its "time", for its HTTP event collector.
func (s *Shipper) format(c config, at time.Time, logger string, level slog.Level, msg string, fields map[string]any) []byte {
	m := map[string]any{}
	for k, v := range fields {
		m[k] = v
	}
	m["@timestamp"] = djangoTime(at)
	m["message"] = msg
	m["host"] = s.host
	m["level"] = levelName(level)
	m["logger_name"] = logger
	m["cluster_host_id"] = s.node
	m["tower_uuid"] = nil
	if c.uuid != "" {
		m["tower_uuid"] = c.uuid
	}
	var out any = m
	if c.kind == "splunk" {
		out = map[string]any{"time": float64(at.UnixMicro()) / 1e6, "event": m}
	}
	b, err := json.Marshal(out)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"@timestamp": m["@timestamp"], "message": msg, "level": m["level"], "logger_name": logger})
	}
	return b
}

// djangoTime is DjangoJSONEncoder's datetime: milliseconds, Z for UTC.
func djangoTime(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

// enqueue keeps rsyslog's action queue rules: at 90% of
// LOG_AGGREGATOR_ACTION_QUEUE_SIZE, INFO and DEBUG messages are discarded
// (queue.discardSeverity 5); a full queue takes no more.
func (s *Shipper) enqueue(c config, e entry) {
	s.mu.Lock()
	n := len(s.queue)
	if n >= c.queueSize || (n >= c.queueSize*9/10 && e.level < slog.LevelWarn) {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, e)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run sends the queued messages until ctx ends, retrying a failed one
// every LOG_AGGREGATOR_TCP_TIMEOUT seconds, as rsyslog's
// action.resumeInterval, and forever, as action.resumeRetryCount -1.
func (s *Shipper) Run(ctx context.Context) {
	for {
		s.mu.Lock()
		var e *entry
		if len(s.queue) > 0 {
			e = &s.queue[0]
		}
		s.mu.Unlock()
		if e == nil {
			select {
			case <-ctx.Done():
				s.closeConn()
				return
			case <-s.wake:
				continue
			}
		}
		c := s.config(ctx)
		if !c.enabled {
			// Turned off: what waited goes nowhere, as rsyslogd's /dev/null.
			s.mu.Lock()
			s.queue = nil
			s.mu.Unlock()
			s.closeConn()
			continue
		}
		err := s.send(ctx, c, e.data)
		var drop permanent
		if err == nil || errors.As(err, &drop) {
			if err != nil {
				s.diag.Warn("log aggregator refused a message", "err", err)
			} else if c.debug {
				s.diag.Info("log aggregator delivery", "protocol", c.protocol, "host", c.host, "bytes", len(e.data))
			}
			s.mu.Lock()
			s.queue = s.queue[1:]
			s.mu.Unlock()
			continue
		}
		s.diag.Warn("log aggregator unreachable", "protocol", c.protocol, "host", c.host, "err", err, "retry_in", c.timeout.String())
		s.closeConn()
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.timeout):
		}
	}
}

// permanent is a refusal retrying will not change (an HTTP 4xx).
type permanent struct{ error }

func (s *Shipper) send(ctx context.Context, c config, data []byte) error {
	if c.protocol == "tcp" || c.protocol == "udp" {
		return s.forward(c, data)
	}
	u, err := httpURL(c.host, c.port)
	if err != nil {
		return permanent{err}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(string(data)))
	if err != nil {
		return permanent{err}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.kind == "splunk" {
		// Splunk's HTTP event collector takes its token in this header.
		if c.password != "" {
			req.Header.Set("Authorization", "Splunk "+c.password)
		}
	} else if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := s.client(c.verify).Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429:
		return permanent{fmt.Errorf("%s answered %s", u, resp.Status)}
	}
	return fmt.Errorf("%s answered %s", u, resp.Status)
}

// forward is omfwd: a TCP connection kept open, a line per message, or a
// UDP datagram per message.
func (s *Shipper) forward(c config, data []byte) error {
	if c.host == "" || c.port == 0 {
		return permanent{fmt.Errorf("Port required for %s", c.protocol)}
	}
	key := c.protocol + "://" + net.JoinHostPort(c.host, strconv.Itoa(c.port))
	if s.conn == nil || s.connKey != key {
		s.closeConn()
		conn, err := net.DialTimeout(c.protocol, net.JoinHostPort(c.host, strconv.Itoa(c.port)), c.timeout)
		if err != nil {
			return err
		}
		s.conn, s.connKey = conn, key
	}
	if c.protocol == "tcp" {
		data = append(append([]byte{}, data...), '\n')
	}
	s.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := s.conn.Write(data)
	return err
}

func (s *Shipper) closeConn() {
	if s.conn != nil {
		s.conn.Close()
		s.conn, s.connKey = nil, ""
	}
}

// httpURL is how AWX builds omhttp's server: https:// unless the host says
// http://, the port from the host, else LOG_AGGREGATOR_PORT, else 443 or
// 80, and the host's path and query.
func httpURL(host string, port int) (string, error) {
	if host == "" {
		return "", errors.New("No logging aggregator host is set.")
	}
	u, err := url.Parse(host)
	if err != nil || (u.Scheme == "" && !strings.HasPrefix(host, "//")) || u.Hostname() == "" {
		if u, err = url.Parse("https://" + host); err != nil {
			return "", err
		}
	}
	scheme := "http"
	if u.Scheme == "https" {
		scheme = "https"
	}
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if port == 0 {
		port = 80
		if scheme == "https" {
			port = 443
		}
	}
	out := url.URL{Scheme: scheme, Host: net.JoinHostPort(u.Hostname(), strconv.Itoa(port)), Path: u.Path, RawQuery: u.RawQuery}
	return out.String(), nil
}

// Test is AWX's /settings/logging/test/: it sends "AWX Connection Test
// Message" at ERROR on the first of LOG_AGGREGATOR_LOGGERS, checks the
// destination, and for TCP and UDP that the port answers.
func (s *Shipper) Test(ctx context.Context) (int, map[string]any) {
	c := s.config(ctx)
	if !c.enabled {
		return http.StatusConflict, map[string]any{"error": "Logging not enabled"}
	}
	logger := "awx"
	if len(c.loggers) > 0 && c.loggers[0] != "awx" {
		logger = "awx.analytics." + c.loggers[0]
	}
	s.Log(ctx, logger, slog.LevelError, "AWX Connection Test Message", nil)
	if c.protocol != "tcp" && c.protocol != "udp" {
		if _, err := httpURL(c.host, c.port); err != nil {
			return http.StatusBadRequest, map[string]any{"error": err.Error()}
		}
		return http.StatusAccepted, nil
	}
	if c.port == 0 {
		return http.StatusBadRequest, map[string]any{"error": "Port required for " + c.protocol}
	}
	conn, err := net.DialTimeout(c.protocol, net.JoinHostPort(c.host, strconv.Itoa(c.port)), 500*time.Millisecond)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": err.Error()}
	}
	conn.Close()
	return http.StatusAccepted, nil
}

// Handler passes the server's log records to next, and ships them as
// AWX's awx logger, their attributes as extra fields.
func (s *Shipper) Handler(next slog.Handler) slog.Handler { return &tee{next: next, s: s} }

type tee struct {
	next  slog.Handler
	s     *Shipper
	attrs []slog.Attr
	group string
}

func (t *tee) Enabled(ctx context.Context, l slog.Level) bool {
	return t.next.Enabled(ctx, l) || t.s.config(ctx).takes("awx.main", l)
}

func (t *tee) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if t.next.Enabled(ctx, r.Level) {
		err = t.next.Handle(ctx, r)
	}
	c := t.s.config(ctx)
	if !c.takes("awx.main", r.Level) {
		return err
	}
	fields := map[string]any{}
	add := func(a slog.Attr) bool {
		k := a.Key
		if t.group != "" {
			k = t.group + "." + k
		}
		v := a.Value.Resolve().Any()
		if e, ok := v.(error); ok {
			v = e.Error()
		}
		fields[k] = v
		return true
	}
	for _, a := range t.attrs {
		add(a)
	}
	r.Attrs(add)
	t.s.enqueue(c, entry{data: t.s.format(c, r.Time, "awx.main", r.Level, r.Message, fields), level: r.Level})
	return err
}

func (t *tee) WithAttrs(as []slog.Attr) slog.Handler {
	return &tee{next: t.next.WithAttrs(as), s: t.s, attrs: append(append([]slog.Attr{}, t.attrs...), as...), group: t.group}
}

func (t *tee) WithGroup(name string) slog.Handler {
	g := name
	if t.group != "" {
		g = t.group + "." + name
	}
	return &tee{next: t.next.WithGroup(name), s: t.s, attrs: t.attrs, group: g}
}

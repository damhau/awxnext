package resources

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// retryDelays are the waits before each retry of a notification that
// failed for a reason that may pass: three more tries over about a minute.
var retryDelays atomic.Pointer[[]time.Duration]

func init() {
	SetNotificationRetryDelays([]time.Duration{5 * time.Second, 15 * time.Second, 40 * time.Second})
}

// SetNotificationRetryDelays sets the waits before each retry, and returns
// the previous ones. Tests shorten them.
func SetNotificationRetryDelays(d []time.Duration) []time.Duration {
	old := retryDelays.Swap(&d)
	if old == nil {
		return nil
	}
	return *old
}

// notificationSweepEvery is how often the leader looks for notifications
// left pending.
const notificationSweepEvery = 15 * time.Second

var lastNotificationSweep atomic.Int64

// maxRetryAfter caps how long a Retry-After answer delays a retry.
const maxRetryAfter = 5 * time.Minute

// staleAfter is how overdue a pending notification's next attempt is before
// the leader takes it over: its server stopped while sending it. One
// pending for longer than staleGiveUp fails instead of being sent late.
const (
	staleAfter  = time.Minute
	staleGiveUp = 24 * time.Hour
)

// retryError is a send error that may pass: a network error, HTTP 429 or
// 5xx, Slack's ratelimited, an SMTP 4xx. after is the wait the server
// asked for, if it did.
type retryError struct {
	err   error
	after time.Duration
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// retryAfterOf is the longest wait the retryable errors in err ask for.
func retryAfterOf(err error) time.Duration {
	var wait time.Duration
	var walk func(error)
	walk = func(e error) {
		var re *retryError
		if errors.As(e, &re) && re.after > wait {
			wait = re.after
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
		}
	}
	walk(err)
	return wait
}

// retryable reports whether any error in err may pass.
func retryable(err error) bool {
	var re *retryError
	return errors.As(err, &re)
}

// retryStatus marks err, the error of an answer with resp's status, as
// worth retrying for a 429 or a 5xx, after its Retry-After.
func retryStatus(err error, resp *http.Response) error {
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &retryError{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
	}
	return err
}

// retryAfter reads a Retry-After header: seconds or an HTTP date.
func retryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	var d time.Duration
	if s, err := strconv.Atoi(h); err == nil {
		d = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = time.Until(t)
	}
	return min(max(d, 0), maxRetryAfter)
}

// retryNetwork marks err, the error of a connection or a request, as worth
// retrying when the network may recover: not for a certificate or a host
// name that doesn't exist.
func retryNetwork(err error) error {
	if err == nil || !transient(err) {
		return err
	}
	return &retryError{err: err}
}

func transient(err error) bool {
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &invalid) || errors.As(err, &record) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTimeout || dns.IsTemporary
	}
	var op *net.OpError
	var ne net.Error
	return errors.As(err, &op) || errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

// retrySMTP marks an email's error as worth retrying: an SMTP 4xx, or a
// network error.
func retrySMTP(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 400 && te.Code < 500 {
		return &retryError{err: err}
	}
	return retryNetwork(err)
}

// notificationRow is a notification as it is sent.
type notificationRow struct {
	id, ntID, jobID int64
	typ, status     string
	event, subject  string
	cfg             map[string]any
	body            json.RawMessage
	addresses       []string
	attempts        int
	sentTo          map[string]map[string]any
}

func loadNotification(ctx context.Context, db api.DBTX, id int64) (*notificationRow, error) {
	n := &notificationRow{id: id}
	err := db.QueryRow(ctx, `SELECT n.notification_template_id, coalesce(n.unified_job_id, 0), n.notification_type, n.status, n.event, n.subject,
		t.notification_configuration, n.body, n.addresses, n.attempts, n.sent_to
		FROM notifications n JOIN notification_templates t ON t.id = n.notification_template_id WHERE n.id = $1`, id).
		Scan(&n.ntID, &n.jobID, &n.typ, &n.status, &n.event, &n.subject, &n.cfg, &n.body, &n.addresses, &n.attempts, &n.sentTo)
	if n.sentTo == nil {
		n.sentTo = map[string]map[string]any{}
	}
	return n, err
}

// deliver sends a notification and records the outcome. An error that may
// pass leaves it pending and tries again, once per retry delay; each
// attempt is claimed, so that only one server makes it.
func deliver(ctx context.Context, app *api.App, id int64) {
	for {
		n, err := loadNotification(ctx, app.DB, id)
		if err != nil || n.status != "pending" {
			return
		}
		delays := *retryDelays.Load()
		if n.attempts > len(delays) {
			finishNotification(app, id, errors.New(errorOr(app, id, "Not sent: the server stopped while sending it.")))
			return
		}
		channels, _ := n.cfg["channels"].([]any)
		lease := 120 + 30*len(channels)
		tag, err := app.DB.Exec(ctx, `UPDATE notifications SET attempts = attempts + 1, next_attempt = now() + make_interval(secs => $3), modified = now()
			WHERE id = $1 AND status = 'pending' AND attempts = $2`, id, n.attempts, lease)
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		n.attempts++
		err = sendNotification(ctx, app, n)
		if err == nil || !retryable(err) || n.attempts > len(delays) {
			finishNotification(app, id, err)
			return
		}
		wait := max(delays[n.attempts-1], retryAfterOf(err))
		slog.Info("notification will be retried", "notification", id, "attempt", n.attempts, "in", wait, "err", err)
		app.DB.Exec(ctx, "UPDATE notifications SET error = $2, next_attempt = now() + make_interval(secs => $3), modified = now() WHERE id = $1",
			id, err.Error(), wait.Seconds())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// errorOr is a notification's recorded error, or else def.
func errorOr(app *api.App, id int64, def string) string {
	var e string
	app.DB.QueryRow(context.Background(), "SELECT error FROM notifications WHERE id = $1", id).Scan(&e)
	if e == "" {
		return def
	}
	return e
}

func finishNotification(app *api.App, id int64, err error) {
	status, errText, sent := "successful", "", 1
	if err != nil {
		status, errText, sent = "failed", err.Error(), 0
		slog.Warn("notification failed", "notification", id, "err", err)
	}
	app.DB.Exec(context.Background(), `UPDATE notifications SET status = $2, error = $3, notifications_sent = $4, next_attempt = NULL, modified = now()
		WHERE id = $1`, id, status, errText, sent)
}

// sendNotification makes one attempt at sending a notification.
func sendNotification(ctx context.Context, app *api.App, n *notificationRow) error {
	switch n.typ {
	case "email":
		cfg := n.cfg
		if len(n.addresses) > 0 {
			cfg = maps.Clone(cfg)
			cfg["recipients"] = toAnyList(n.addresses)
		}
		var text string
		if json.Unmarshal(n.body, &text) != nil {
			text = string(n.body)
		}
		return retrySMTP(sendEmail(ctx, app, cfg, n.subject, text))
	case "slack":
		return sendSlack(ctx, app, n)
	case "teams":
		return sendTeams(ctx, app, n.cfg, n.body)
	default:
		return sendWebhook(ctx, app, n.cfg, n.body)
	}
}

// ordered holds the notifications that go out in order, per notification
// template, as a catalog request's events do: one being retried holds back
// the template's later ones instead of letting them overtake it.
var ordered = struct {
	sync.Mutex
	queues map[int64][]int64
}{queues: map[int64][]int64{}}

// deliverInOrder sends notifications in the order given, one at a time per
// notification template.
func deliverInOrder(app *api.App, ids []int64) {
	if len(ids) == 0 {
		return
	}
	rows, err := app.DB.Query(context.Background(), "SELECT id, notification_template_id FROM notifications WHERE id = ANY($1)", ids)
	if err != nil {
		slog.Warn("notifications to send in order", "err", err)
		return
	}
	tpl := map[int64]int64{}
	for rows.Next() {
		var id, nt int64
		if rows.Scan(&id, &nt) == nil {
			tpl[id] = nt
		}
	}
	rows.Close()
	ordered.Lock()
	defer ordered.Unlock()
	for _, id := range ids {
		nt, ok := tpl[id]
		if !ok {
			continue
		}
		q, sending := ordered.queues[nt]
		ordered.queues[nt] = append(q, id)
		if !sending {
			go sendQueue(app, nt)
		}
	}
}

func sendQueue(app *api.App, nt int64) {
	for {
		ordered.Lock()
		q := ordered.queues[nt]
		if len(q) == 0 {
			delete(ordered.queues, nt)
			ordered.Unlock()
			return
		}
		ordered.queues[nt] = q[1:]
		ordered.Unlock()
		deliver(context.Background(), app, q[0])
	}
}

// NotificationTick, on the leader, takes over the notifications whose
// server stopped while sending them, and fails those pending for a day. It
// takes a template's oldest pending notification only, so that those sent
// in order stay in order.
func NotificationTick(ctx context.Context, m *jobs.Manager) {
	now := time.Now()
	if now.Sub(time.Unix(0, lastNotificationSweep.Load())) < notificationSweepEvery {
		return
	}
	lastNotificationSweep.Store(now.UnixNano())
	m.DB().Exec(ctx, `UPDATE notifications SET status = 'failed', next_attempt = NULL, modified = now(),
		error = CASE WHEN error = '' THEN 'Not sent: the server stopped while sending it.' ELSE error END
		WHERE status = 'pending' AND next_attempt < now() - make_interval(secs => $1)`, staleGiveUp.Seconds())
	rows, err := m.DB().Query(ctx, `UPDATE notifications SET next_attempt = now() + interval '5 minutes', modified = now()
		WHERE id IN (SELECT id FROM notifications n WHERE status = 'pending' AND next_attempt < now() - make_interval(secs => $1)
			AND NOT EXISTS (SELECT 1 FROM notifications e WHERE e.notification_template_id = n.notification_template_id
				AND e.status = 'pending' AND e.next_attempt IS NOT NULL AND e.id < n.id)
			ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED) RETURNING id`, staleAfter.Seconds())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		slog.Info("resuming a pending notification", "notification", id)
		go deliver(context.Background(), m.App, id)
	}
}

// resendNotification records a failed notification again, as a new one
// that keeps what each Slack channel already got.
func resendNotification(ctx context.Context, db api.DBTX, id int64) (int64, error) {
	var sentTo map[string]map[string]any
	if err := db.QueryRow(ctx, "SELECT sent_to FROM notifications WHERE id = $1", id).Scan(&sentTo); err != nil {
		return 0, err
	}
	kept := map[string]map[string]any{}
	for ch, r := range sentTo {
		if str(r["ts"]) != "" {
			kept[ch] = r
		}
	}
	var nid int64
	err := db.QueryRow(ctx, `INSERT INTO notifications (notification_template_id, unified_job_id, notification_type, recipients, subject, body, addresses, event, sent_to)
		SELECT notification_template_id, unified_job_id, notification_type, recipients, subject, body, addresses, event, $2 FROM notifications WHERE id = $1
		RETURNING id`, id, kept).Scan(&nid)
	return nid, err
}

// failingSummary is summary_fields.failing of a notification template: the
// sends that failed since its last success, once there are failingAfter.
func failingSummary(c *Ctx, nt any) (any, error) {
	var n int
	var last string
	var since *time.Time
	err := c.DB().QueryRow(c, `SELECT count(*), coalesce((array_agg(error ORDER BY id DESC))[1], ''), min(created) FROM notifications
		WHERE notification_template_id = $1 AND status = 'failed' AND id > coalesce((SELECT max(id) FROM notifications
			WHERE notification_template_id = $1 AND status = 'successful'), 0)`, nt).Scan(&n, &last, &since)
	if err != nil || n < failingAfter {
		return nil, err
	}
	return Obj{"failures": n, "error": last, "since": api.FormatTime(*since)}, nil
}

// failingAfter is how many failed sends in a row mark a notification
// template failing.
const failingAfter = 3

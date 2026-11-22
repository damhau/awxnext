package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// Slack notifications, as AWX's Slack backend: a chat.postMessage per
// channel with the bot token, the message as the text.

// slackFields are AWX's Slack notification_configuration fields. hex_color
// isn't one of AWX's init_parameters, so AWX stores it only when it is set.
// thread_failures is awxnext's: one thread per failure of a template.
var slackFields = []configField{
	{name: "token", typ: "password"},
	{name: "channels", typ: "list"},
	{name: "hex_color", typ: "string", optional: true},
	{name: "thread_failures", typ: "bool", optional: true},
}

// SlackAPIURL is the Slack Web API's base URL. Tests point it at a fake.
var SlackAPIURL = "https://slack.com/api/"

// validateSlackConfig checks a Slack configuration as AWX does and encrypts
// its token. A hex_color that isn't #RGB or #RRGGBB is kept and, as in AWX,
// sends without a color.
func validateSlackConfig(c *Ctx, cfg, old map[string]any) (map[string]any, error) {
	out, err := checkConfigFields("slack", slackFields, cfg)
	if err != nil {
		return nil, err
	}
	channels := out["channels"].([]any)
	if len(channels) == 0 {
		return nil, api.FieldError("notification_configuration", "No values specified for field 'channels'")
	}
	for _, ch := range channels {
		if _, _, ok := slackTarget(ch.(string)); !ok {
			return nil, api.FieldError("notification_configuration", fmt.Sprintf("%q is not a channel: write #name, or #name, <message ts> to post in that message's thread.", ch))
		}
	}
	if err := sealSecret(c, out, old, "token"); err != nil {
		return nil, err
	}
	if out["token"] == "" {
		return nil, api.FieldError("notification_configuration", "token must not be empty.")
	}
	return out, nil
}

// slackTarget reads a channel as AWX does: "#name", or "#name, <ts>" to post
// in the thread of the message with that ts. AWX sends what follows the
// comma as it is; the spaces around it are trimmed here.
func slackTarget(s string) (channel, thread string, ok bool) {
	channel, thread, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "#"), ",")
	channel, thread = strings.TrimSpace(channel), strings.TrimSpace(thread)
	return channel, thread, channel != "" && !strings.ContainsAny(channel, " #") && !strings.Contains(thread, ",")
}

// slackColor is AWX's test of hex_color: a # and 3 or 6 more characters.
func slackColor(cfg map[string]any) string {
	c := str(cfg["hex_color"])
	if strings.HasPrefix(c, "#") && (len(c) == 4 || len(c) == 7) {
		return c
	}
	return ""
}

type slackAttachment struct {
	Color string `json:"color"`
	Text  string `json:"text"`
}

type slackMessage struct {
	Channel     string            `json:"channel"`
	ThreadTS    string            `json:"thread_ts,omitempty"`
	AsUser      bool              `json:"as_user"`
	Text        string            `json:"text,omitempty"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

// slackMessageOf is a message as AWX posts it: a colored attachment when the
// configuration has a color, else text.
func slackMessageOf(channel, thread, text, color string) slackMessage {
	msg := slackMessage{Channel: channel, ThreadTS: thread, AsUser: true}
	if color != "" {
		msg.Attachments = []slackAttachment{{Color: color, Text: text}}
	} else {
		msg.Text = text
	}
	return msg
}

// slackTransient are Slack's errors that may pass.
var slackTransient = []string{"ratelimited", "service_unavailable", "request_timeout", "internal_error"}

// sendSlack posts the message to each channel it hasn't reached yet, and
// records what each got. Unlike AWX, which stops at the first channel Slack
// refuses, it posts to the others and reports each refusal. A refusal that
// may pass is tried again with the notification; another is final.
func sendSlack(ctx context.Context, app *api.App, n *notificationRow) error {
	token, err := openSecret(app, n.cfg, "token")
	if err != nil {
		return err
	}
	color := slackColor(n.cfg)
	text := n.subject
	th := slackThreadOf(ctx, app, n)
	channels, _ := n.cfg["channels"].([]any)
	var errs []error
	for _, ch := range channels {
		raw := strings.TrimPrefix(strings.TrimSpace(str(ch)), "#")
		if r, done := n.sentTo[raw]; done {
			if e := str(r["error"]); e != "" {
				errs = append(errs, fmt.Errorf("Slack Notification unable to send %s: %s (%s)", raw, text, e))
			}
			continue
		}
		channel, thread, _ := slackTarget(raw)
		var sent map[string]any
		var err error
		if th != nil && thread == "" {
			sent, err = th.post(ctx, app, token, channel, text, color)
		} else {
			var a slackAnswer
			if a, err = postSlack(ctx, token, "chat.postMessage", slackMessageOf(channel, thread, text, color)); err == nil {
				sent = map[string]any{"channel": a.Channel, "ts": a.TS}
			}
		}
		if err != nil {
			if !retryable(err) {
				recordSent(ctx, app, n.id, raw, map[string]any{"error": err.Error()})
			}
			errs = append(errs, fmt.Errorf("Slack Notification unable to send %s: %s (%w)", raw, text, err))
			continue
		}
		recordSent(ctx, app, n.id, raw, sent)
	}
	return errors.Join(errs...)
}

// recordSent records what a channel got: its message's ts, or Slack's
// final refusal.
func recordSent(ctx context.Context, app *api.App, id int64, channel string, r map[string]any) {
	if _, err := app.DB.Exec(ctx, "UPDATE notifications SET sent_to = sent_to || jsonb_build_object($2::text, $3::jsonb) WHERE id = $1", id, channel, r); err != nil {
		slog.Error("recording a Slack post", "notification", id, "err", err)
	}
}

type slackAnswer struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// postSlack calls a Web API method, such as chat.postMessage, and returns
// Slack's answer or its error, such as channel_not_found or invalid_auth.
func postSlack(ctx context.Context, token, method string, msg any) (slackAnswer, error) {
	var out slackAnswer
	b, err := json.Marshal(msg)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SlackAPIURL+method, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", api.ProductName+"/"+api.ProductVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, retryNetwork(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, retryStatus(fmt.Errorf("HTTP %s", resp.Status), resp)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("unreadable response: %v", err)
	}
	if !out.OK {
		err := errors.New(out.Error)
		if slices.Contains(slackTransient, out.Error) {
			return out, &retryError{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return out, err
	}
	return out, nil
}

// slackThreadEvents are the events of a finished run: those a failure
// thread holds.
var slackThreadEvents = []string{"success", "error", "first_error", "recovered", "slow"}

// slackThread is a notification's place in its template's failure threads:
// the run's template, and whether the run failed or succeeded.
type slackThread struct {
	ntID, ujt, jobID int64
	failure          bool
	recovered        string
}

// slackThreadOf is the failure thread a notification belongs to, or nil:
// with thread_failures on, a finished run's event, of a template, that
// failed or succeeded. A slice of a sliced job and an approval aren't
// threaded.
func slackThreadOf(ctx context.Context, app *api.App, n *notificationRow) *slackThread {
	if !boolv(n.cfg["thread_failures"]) || n.jobID == 0 || !slices.Contains(slackThreadEvents, n.event) {
		return nil
	}
	var typ, status string
	var ujt int64
	var sliceCount int
	if app.DB.QueryRow(ctx, "SELECT type, status, coalesce(unified_job_template_id, 0), job_slice_count FROM unified_jobs WHERE id = $1", n.jobID).
		Scan(&typ, &status, &ujt, &sliceCount) != nil || ujt == 0 || typ == "workflow_approval" || typ == "job" && sliceCount > 1 {
		return nil
	}
	th := &slackThread{ntID: n.ntID, ujt: ujt, jobID: n.jobID,
		recovered: fmt.Sprintf("Recovered by %s #%d: %s", friendlyNames[typ], n.jobID, JobUIURL(app, n.jobID))}
	switch status {
	case "failed", "error":
		th.failure = true
	case "successful":
	default:
		return nil
	}
	return th
}

// post posts a message in the channel's open failure thread. A failure with
// no thread starts one. A success with one replies in it, edits its first
// message to say which run recovered, and closes it; one without posts to
// the channel. The thread's row stays locked meanwhile, so that the
// notifications of a run, and runs that end at once, take turns.
func (th *slackThread) post(ctx context.Context, app *api.App, token, channel, text, color string) (map[string]any, error) {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	key := []any{th.ntID, th.ujt, channel}
	if th.failure {
		if _, err := tx.Exec(ctx, `INSERT INTO slack_threads (notification_template_id, unified_job_template_id, channel) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, key...); err != nil {
			return nil, err
		}
	}
	var channelID, ts, first string
	err = tx.QueryRow(ctx, `SELECT channel_id, ts, text FROM slack_threads WHERE notification_template_id = $1 AND unified_job_template_id = $2
		AND channel = $3 FOR UPDATE`, key...).Scan(&channelID, &ts, &first)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	msg := slackMessageOf(channel, "", text, color)
	if ts != "" {
		msg.Channel, msg.ThreadTS = channelID, ts
	}
	a, err := postSlack(ctx, token, "chat.postMessage", msg)
	if err != nil {
		return nil, err
	}
	sent := map[string]any{"channel": a.Channel, "ts": a.TS}
	switch {
	case th.failure && ts == "":
		_, err = tx.Exec(ctx, `UPDATE slack_threads SET channel_id = $4, ts = $5, text = $6, job_id = $7, created = now()
			WHERE notification_template_id = $1 AND unified_job_template_id = $2 AND channel = $3`, append(key, a.Channel, a.TS, text, th.jobID)...)
		sent["thread"] = "started"
	case th.failure:
		sent["thread"] = ts
	case ts != "":
		sent["thread"] = ts
		edit := map[string]any{"channel": channelID, "ts": ts}
		if line := first + "\n" + th.recovered; color != "" {
			edit["attachments"] = []slackAttachment{{Color: color, Text: line}}
		} else {
			edit["text"] = line
		}
		if _, err := postSlack(ctx, token, "chat.update", edit); err != nil {
			slog.Warn("editing the first message of a Slack thread", "notification_template", th.ntID, "err", err)
			sent["edit_error"] = err.Error()
		}
		_, err = tx.Exec(ctx, "DELETE FROM slack_threads WHERE notification_template_id = $1 AND unified_job_template_id = $2 AND channel = $3", key...)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.Error("recording a Slack thread", "notification_template", th.ntID, "err", err)
	}
	return sent, nil
}

// closeSlackThreads ends the open failure threads of a run's template whose
// notification template sent nothing for this run, which recovered: they
// get its recovered message, so that each thread ends with the recovery.
func closeSlackThreads(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	ujt := row.Int("unified_job_template_id")
	if row.Str("status") != "successful" || ujt == 0 || row.Str("type") == "workflow_approval" || row.Str("type") == "job" && row.Int("job_slice_count") > 1 {
		return
	}
	rows, err := m.DB().Query(ctx, `SELECT DISTINCT t.notification_template_id FROM slack_threads t
		JOIN notification_templates n ON n.id = t.notification_template_id
		WHERE t.unified_job_template_id = $1 AND t.ts <> '' AND n.notification_type = 'slack' AND n.notification_configuration @> '{"thread_failures": true}'
			AND NOT EXISTS (SELECT 1 FROM notifications x WHERE x.notification_template_id = t.notification_template_id AND x.unified_job_id = $2
				AND x.event = ANY($3))
		ORDER BY 1`, ujt, row.ID(), slackThreadEvents)
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
	sendJobNotifications(ctx, m, row, "recovered", nil, ids)
}

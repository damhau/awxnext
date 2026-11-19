package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

// Slack notifications, as AWX's Slack backend: a chat.postMessage per
// channel with the bot token, the message as the text.

// slackFields are AWX's Slack notification_configuration fields. hex_color
// isn't one of AWX's init_parameters, so AWX stores it only when it is set.
var slackFields = []configField{
	{name: "token", typ: "password"},
	{name: "channels", typ: "list"},
	{name: "hex_color", typ: "string", optional: true},
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

// sendSlack posts the message to each channel, as a colored attachment when
// the configuration has a color. Unlike AWX, which stops at the first
// channel Slack refuses, it posts to the others and reports each refusal.
func sendSlack(ctx context.Context, app *api.App, cfg map[string]any, text string) error {
	token, err := openSecret(app, cfg, "token")
	if err != nil {
		return err
	}
	color := slackColor(cfg)
	channels, _ := cfg["channels"].([]any)
	var errs []error
	for _, ch := range channels {
		raw := strings.TrimPrefix(strings.TrimSpace(str(ch)), "#")
		channel, thread, _ := slackTarget(raw)
		msg := slackMessage{Channel: channel, ThreadTS: thread, AsUser: true}
		if color != "" {
			msg.Attachments = []slackAttachment{{Color: color, Text: text}}
		} else {
			msg.Text = text
		}
		if err := postSlack(ctx, token, msg); err != nil {
			errs = append(errs, fmt.Errorf("Slack Notification unable to send %s: %s (%v)", raw, text, err))
		}
	}
	return errors.Join(errs...)
}

// postSlack calls chat.postMessage and returns Slack's error, such as
// channel_not_found or invalid_auth.
func postSlack(ctx context.Context, token string, msg slackMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SlackAPIURL+"chat.postMessage", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", api.ProductName+"/"+api.ProductVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("unreadable response: %v", err)
	}
	if !out.OK {
		return errors.New(out.Error)
	}
	return nil
}

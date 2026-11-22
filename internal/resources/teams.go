package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
)

// Microsoft Teams notifications are awxnext's own type, since AWX has none:
// an Adaptive Card posted to the webhook URL of a Teams workflow.

// teamsFields is the teams notification_configuration: the workflow's URL,
// a secret, since its signature authorizes the post.
var teamsFields = []configField{{name: "url", typ: "password"}}

// validateTeamsConfig checks a Teams configuration and encrypts its URL.
func validateTeamsConfig(c *Ctx, cfg, old map[string]any) (map[string]any, error) {
	out, err := checkConfigFields("teams", teamsFields, cfg)
	if err != nil {
		return nil, err
	}
	u := out["url"].(string)
	if u == crypto.Placeholder {
		u, _ = old["url"].(string)
	}
	if !crypto.IsEncrypted(u) {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return nil, api.FieldError("notification_configuration", "url must be the http(s) URL of the Teams workflow.")
		}
	}
	if err := sealSecret(c, out, old, "url"); err != nil {
		return nil, err
	}
	return out, nil
}

// teamsCard is what a Teams card shows under its title: facts, and a
// button that opens link.
type teamsCard struct {
	color        string
	facts        [][2]string
	link, action string
}

// teamsMessage is the body of a post to a Teams workflow: the message as an
// Adaptive Card, which the workflow posts to its channel.
func teamsMessage(title string, card teamsCard) map[string]any {
	head := map[string]any{"type": "TextBlock", "text": title, "weight": "Bolder", "size": "Medium", "wrap": true}
	if card.color != "" {
		head["color"] = card.color
	}
	body := []any{head}
	var facts []any
	for _, f := range card.facts {
		if f[1] != "" {
			facts = append(facts, map[string]any{"title": f[0], "value": f[1]})
		}
	}
	if len(facts) > 0 {
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}
	content := map[string]any{"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
		"msteams": map[string]any{"width": "Full"}, "body": body}
	if strings.HasPrefix(card.link, "http://") || strings.HasPrefix(card.link, "https://") {
		content["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": card.action, "url": card.link}}
	}
	return map[string]any{"type": "message", "attachments": []any{
		map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil, "content": content}}}
}

// teamsJobCard shows a job's status, template, launcher and times, with a
// button to its output.
func teamsJobCard(job, meta map[string]any) teamsCard {
	status := str(meta["status"])
	tpl := str(meta["name"])
	launcher := str(meta["created_by"])
	if sf, ok := job["summary_fields"].(map[string]any); ok {
		if u, ok := sf["unified_job_template"].(map[string]any); ok && str(u["name"]) != "" {
			tpl = str(u["name"])
		}
	}
	if lb, ok := job["launched_by"].(map[string]any); ok && str(lb["name"]) != "" {
		launcher = str(lb["name"])
	}
	var elapsed string
	if e, ok := num(job["elapsed"]); ok && str(meta["finished"]) != "" {
		elapsed = teamsDuration(e)
	}
	return teamsCard{
		color: map[string]string{"successful": "Good", "failed": "Attention", "error": "Attention", "canceled": "Warning"}[status],
		facts: [][2]string{{"Status", status}, {"Template", tpl}, {"Launched by", launcher},
			{"Started", teamsTime(meta["started"])}, {"Finished", teamsTime(meta["finished"])}, {"Elapsed", elapsed}},
		link:   str(meta["url"]),
		action: "Open the job",
	}
}

// teamsApprovalCard shows an approval's status and workflow. Its button
// opens the approvals while it waits, and the workflow once decided.
func teamsApprovalCard(name, status, workflow, link string) teamsCard {
	label := map[string]string{"running": "Waiting for approval", "approved": "Approved", "denied": "Denied", "timed_out": "Timed out"}[status]
	action := "Open the workflow job"
	if status == "running" {
		action = "Review the approval"
	}
	return teamsCard{
		color:  map[string]string{"running": "Warning", "approved": "Good", "denied": "Attention", "timed_out": "Attention"}[status],
		facts:  [][2]string{{"Status", label}, {"Approval", name}, {"Workflow", workflow}},
		link:   link,
		action: action,
	}
}

// teamsDuration shows seconds to the second, as 1m5s.
func teamsDuration(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(time.Second).String()
}

// teamsTime shows an API time in UTC, to the second.
func teamsTime(v any) string {
	t, err := time.Parse(time.RFC3339Nano, str(v))
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// sendTeams posts a notification's card to the Teams workflow. Its errors
// never hold the URL, whose signature authorizes posting.
func sendTeams(ctx context.Context, app *api.App, cfg map[string]any, body []byte) error {
	u, err := openSecret(app, cfg, "url")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return errors.New("Teams notification failed: the workflow URL is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", api.ProductName+"/"+api.ProductVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return retryNetwork(fmt.Errorf("Teams notification failed: %w", err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := redactQuery(teamsError(b, resp.Header.Get("Content-Type")), req.URL)
		if msg == "" {
			return retryStatus(fmt.Errorf("Teams answered %s", resp.Status), resp)
		}
		return retryStatus(fmt.Errorf("Teams answered %s: %s", resp.Status, msg), resp)
	}
	return nil
}

// teamsError is the message of a workflow's error answer, as Power
// Automate's {"error": {"code", "message"}}, or else its plain text. An
// HTML page, as a proxy's, gives none.
func teamsError(b []byte, contentType string) string {
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &out) == nil {
		switch c, m := out.Error.Code, out.Error.Message; {
		case c != "" && m != "":
			return c + ": " + m
		case c != "" || m != "":
			return c + m
		}
	}
	if !strings.HasPrefix(contentType, "text/plain") {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// redactQuery hides the URL's query values, such as its signature, should
// an answer quote them.
func redactQuery(s string, u *url.URL) string {
	for _, vs := range u.Query() {
		for _, v := range vs {
			if len(v) >= 8 {
				s = strings.ReplaceAll(s, v, "REDACTED")
			}
		}
	}
	return s
}

// Package eventstreams receives events that outside systems post to a
// stream (roadmap M14-01): it reads their formats as CloudEvents, checks
// their senders' credentials, and keeps them in the event log, which rules
// read each at its own position.
package eventstreams

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// The stream types: the format a stream's senders post.
const (
	TypeCloudEvents  = "cloudevents"
	TypeGeneric      = "generic"
	TypeAlertmanager = "alertmanager"
	TypeGitHub       = "github"
	TypeGitLab       = "gitlab"
	TypeGrafana      = "grafana"
	TypeServiceNow   = "servicenow"
)

// Types lists the stream types and their labels.
var Types = [][2]string{
	{TypeCloudEvents, "CloudEvents"},
	{TypeGeneric, "Generic JSON"},
	{TypeAlertmanager, "Prometheus Alertmanager"},
	{TypeGitHub, "GitHub webhook"},
	{TypeGitLab, "GitLab webhook"},
	{TypeGrafana, "Grafana alerting"},
	{TypeServiceNow, "ServiceNow"},
}

// Event is a received event, as a CloudEvent.
type Event struct {
	ID              string
	Source          string
	Type            string
	Subject         string
	Time            *time.Time
	DataContentType string
	DataSchema      string
	// Data is the event's data as JSON: the JSON a sender posted, or a
	// string holding its text.
	Data       json.RawMessage
	Extensions map[string]any
}

// The longest attributes accepted.
const (
	maxIDLength      = 1024
	maxSourceLength  = 2048
	maxTypeLength    = 1024
	maxSubjectLength = 2048
)

func (e *Event) check() error {
	for _, a := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"id", e.ID, maxIDLength, true},
		{"source", e.Source, maxSourceLength, true},
		{"type", e.Type, maxTypeLength, true},
		{"subject", e.Subject, maxSubjectLength, false},
	} {
		if a.required && a.value == "" {
			return badRequest("An event has no %s.", a.name)
		}
		if len(a.value) > a.max {
			return badRequest("An event's %s is longer than %d bytes.", a.name, a.max)
		}
	}
	return nil
}

// Error is a refused request: its HTTP status and why, for the sender.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func badRequest(format string, a ...any) error {
	return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, a...)}
}

func unsupported(format string, a ...any) error {
	return &Error{Status: http.StatusUnsupportedMediaType, Message: fmt.Sprintf(format, a...)}
}

var (
	errUnauthorized = &Error{Status: http.StatusUnauthorized, Message: "Authentication failed."}
	errClaims       = &Error{Status: http.StatusForbidden, Message: "The token does not meet the stream's conditions."}
)

package resources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/jsonpath"
)

// Event streams (awxnext, roadmap M14-01): where outside systems post
// events, each stream at its own URL, with its own credential, format and
// retention. What they post is kept in the event log (stream_events),
// which the rules of later tasks read.

const (
	minStreamPayload = 1 << 10
	maxStreamPayload = 25 << 20
	// ingestTimeout bounds storing a request's events: past it the sender
	// is told to retry rather than kept waiting.
	ingestTimeout = 10 * time.Second
)

var streamLimiter = eventstreams.NewLimiter()

func streamTypeChoices() []api.Choice {
	out := make([]api.Choice, 0, len(eventstreams.Types))
	for _, t := range eventstreams.Types {
		out = append(out, api.Choice{Value: t[0], Label: t[1]})
	}
	return out
}

func between(min, max int64) func(c *Ctx, v any, data Obj) error {
	return func(c *Ctx, v any, data Obj) error {
		n, _ := api.ID(v)
		if n < min || n > max {
			return fmt.Errorf("Ensure this value is between %d and %d.", min, max)
		}
		return nil
	}
}

func validateStreamMapping(c *Ctx, v any, data Obj) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("Expected an object of JSONPaths.")
	}
	for k, p := range m {
		known := false
		for _, key := range eventstreams.MappingKeys {
			known = known || key == k
		}
		if !known {
			return fmt.Errorf("%s is not an attribute: map %s.", k, strings.Join(eventstreams.MappingKeys, ", "))
		}
		if s, ok := p.(string); !ok || !jsonpath.Valid(s) {
			return fmt.Errorf("Invalid JSONPath %v for %s (use $.a.b[0]['c']).", p, k)
		}
	}
	return nil
}

// ReceiverPath is where senders post a stream's events.
func ReceiverPath(uuid string) string { return "/api/v2/event_streams/" + uuid + "/post/" }

func serviceBase(c *Ctx) string {
	if base := c.App.Settings.String(c, "TOWER_URL_BASE"); base != "" {
		return strings.TrimRight(base, "/")
	}
	return strings.TrimRight(c.App.Cfg.BaseURL, "/")
}

func registerEventStreams() {
	streams := api.Register(&Resource{
		Name:        "event_streams",
		Type:        "event_stream",
		Table:       "event_streams",
		Verbose:     "Event Stream",
		Ordering:    "name",
		ContentType: "eventstream",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "stream_type", Type: api.TChoice, Default: eventstreams.TypeGeneric, Choices: streamTypeChoices(),
				Help: "The format the stream's senders post. Each event becomes a CloudEvent."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true,
				Help: "What senders authenticate with: a credential of an Event Stream type."},
			{Name: "test_mode", Type: api.TBool, Default: false, Help: "Keep and show the stream's events, but don't give them to rules."},
			{Name: "retention_days", Type: api.TInt, Default: 7, Validate: between(1, 3650), Help: "Days the stream's events are kept."},
			{Name: "max_payload_bytes", Type: api.TInt, Default: 1 << 20, Validate: between(minStreamPayload, maxStreamPayload),
				Help: "The largest request the stream takes, in bytes."},
			{Name: "rate_limit", Type: api.TInt, Default: 6000, Validate: atLeast(0),
				Help: "Most events per minute the stream takes on each node, which may arrive at once. 0: no limit."},
			{Name: "mapping", Type: api.TJSON, Default: Obj{}, Validate: validateStreamMapping,
				Help: "For a generic stream, the JSONPaths of its events' id, type, source, subject and time in the payload."},
			{Name: "uuid", Type: api.TString, ReadOnly: true},
			{Name: "last_sequence", Type: api.TInt, ReadOnly: true, Help: "The number of the stream's last event."},
			{Name: "last_event_at", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			v, given := data["credential"]
			if !given && old != nil {
				return nil
			}
			if old != nil && fmt.Sprint(v) == fmt.Sprint(old["credential"]) {
				return nil
			}
			id, ok := api.ID(v)
			if !ok {
				return api.FieldError("credential", "An event stream needs a credential of an Event Stream type.")
			}
			var ns *string
			if err := c.DB().QueryRow(c, `SELECT ct.namespace FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id
				WHERE cr.id = $1`, id).Scan(&ns); err != nil {
				return api.FieldError("credential", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
			}
			if ns == nil || !eventstreams.IsStreamCredential(*ns) {
				return api.FieldError("credential", "The credential must be of an Event Stream type.")
			}
			return api.RequirePermID(c, "credential", "credentials", id, "use")
		},
		Serialize: func(c *Ctx, o Obj) {
			path := ReceiverPath(str(o["uuid"]))
			o["receiver_url"] = serviceBase(c) + path
			if rel, ok := o["related"].(Obj); ok {
				rel["receiver"] = path
			}
		},
		SubLists: []*SubList{streamEventsSub()},
	})

	api.Register(&Resource{
		Name:         "stream_events",
		Type:         "stream_event",
		Table:        "stream_events",
		Verbose:      "Stream Event",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "id",
		SearchFields: []string{"event_type", "subject", "source", "event_id"},
		Fields: []*Field{
			{Name: "stream", Type: api.TFK, Ref: "event_streams", ReadOnly: true},
			{Name: "sequence", Type: api.TInt, ReadOnly: true, Help: "The event's number in its stream."},
			{Name: "received", Type: api.TDateTime, ReadOnly: true},
			{Name: "event_id", Type: api.TString, ReadOnly: true, Help: "The CloudEvent's id."},
			{Name: "source", Type: api.TString, ReadOnly: true},
			{Name: "event_type", Type: api.TString, ReadOnly: true, Help: "The CloudEvent's type."},
			{Name: "subject", Type: api.TString, ReadOnly: true},
			{Name: "event_time", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When the event happened, as its sender says."},
			{Name: "data_content_type", Type: api.TString, ReadOnly: true},
			{Name: "data_schema", Type: api.TString, ReadOnly: true},
			{Name: "data", Type: api.TJSON, ReadOnly: true, Nullable: true},
			{Name: "extensions", Type: api.TJSON, ReadOnly: true},
			{Name: "test_mode", Type: api.TBool, ReadOnly: true, Help: "Received in test mode: rules don't see it."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.stream_id IN (SELECT t.id FROM event_streams t WHERE %s)", api.ViewPredicate(c, streams, arg))
		},
	})
}

func streamEventsSub() *SubList {
	s := readOnlyChildSub("events", "stream_events", "stream_id")
	s.Order = func(int64) string { return "t.sequence" }
	return s
}

// installEventStreamRoutes adds the receiver, whose callers are never users
// but senders with the stream's credential, and the backtest and the
// editor's check of a draft rule.
func installEventStreamRoutes(app *api.App) {
	app.HandleAnonymous("POST", "/api/v2/event_streams/{uuid}/post/", receiveEvents)
	app.Handle("POST", "/api/v2/event_rules/backtest/", backtestDraft)
	app.Handle("POST", "/api/v2/event_rules/check/", checkDraft)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func writeStreamError(w http.ResponseWriter, status int, msg string) {
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "5")
	}
	api.WriteJSON(w, status, Obj{"detail": msg})
}

func unavailable(w http.ResponseWriter, what string, err error) {
	slog.Warn("event stream: "+what, "err", err)
	writeStreamError(w, http.StatusServiceUnavailable, "The event could not be stored. Try again later.")
}

// receiveEvents takes a sender's events. 202 means they are committed to
// the log; on 503 nothing is, and the sender may send them again.
func receiveEvents(c *Ctx, rw http.ResponseWriter, r *http.Request) {
	w := &statusRecorder{ResponseWriter: rw}
	stream := ""
	defer func() {
		if w.status >= 400 {
			streamRefused.With(stream, refusal(w.status)).Inc()
		}
	}()
	uuid := strings.ToLower(chi.URLParam(r, "uuid"))
	if !uuidRe.MatchString(uuid) {
		writeStreamError(w, http.StatusNotFound, "Not found.")
		return
	}
	var s struct {
		id                  int64
		typ                 string
		credential          *int64
		test                bool
		maxBytes, perMinute int
		mapping             map[string]any
	}
	err := c.App.DB.QueryRow(c, `SELECT id, stream_type, credential_id, test_mode, max_payload_bytes, rate_limit, mapping
		FROM event_streams WHERE uuid = $1`, uuid).Scan(&s.id, &s.typ, &s.credential, &s.test, &s.maxBytes, &s.perMinute, &s.mapping)
	if errors.Is(err, pgx.ErrNoRows) {
		writeStreamError(w, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		unavailable(w, "reading the stream", err)
		return
	}
	stream = idLabel(s.id)
	tooLarge := fmt.Sprintf("This stream takes requests of at most %d bytes.", s.maxBytes)
	if r.ContentLength > int64(s.maxBytes) {
		writeStreamError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, int64(s.maxBytes)))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeStreamError(w, http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		writeStreamError(w, http.StatusBadRequest, "The request's body could not be read.")
		return
	}
	connector, byConnector, err := connectorPoster(c, r, s.id)
	if err != nil {
		unavailable(w, "reading the stream's connectors", err)
		return
	}
	switch {
	case byConnector && connector == 0:
		writeStreamError(w, http.StatusUnauthorized, "Authentication failed.")
		return
	case byConnector:
		// Connectors post CloudEvents batches, whatever the stream's type.
		s.typ = eventstreams.TypeCloudEvents
	case s.credential == nil:
		writeStreamError(w, http.StatusUnauthorized, "Authentication failed.")
		return
	default:
		cred, err := ResolveCredential(c, c.App, *s.credential)
		if err != nil {
			unavailable(w, "reading the stream's credential", err)
			return
		}
		if err := eventstreams.Verify(c, streamPolicy(c), &eventstreams.Credential{Namespace: cred.Namespace, Inputs: cred.Inputs}, r, body); err != nil {
			writeEventsError(w, err)
			return
		}
	}
	mapping := map[string]string{}
	for k, v := range s.mapping {
		mapping[k] = str(v)
	}
	events, err := eventstreams.Parse(s.typ, eventstreams.Request{Header: r.Header, Body: body, Source: "/api/v2/event_streams/" + strconv.FormatInt(s.id, 10) + "/", Mapping: mapping})
	if err != nil {
		writeEventsError(w, err)
		return
	}
	if byConnector {
		// The source names the connector, whatever it says: duplicates are
		// found per connector.
		for i := range events {
			events[i].Source = "/api/v2/event_connectors/" + strconv.FormatInt(connector, 10) + "/"
		}
	}
	eventstreams.AddTraceContext(events, r.Header)
	wait, ok := streamLimiter.Take(s.id, s.perMinute, len(events), time.Now())
	if !ok {
		writeStreamError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("This stream takes at most %d events at once.", s.perMinute))
		return
	}
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeStreamError(w, http.StatusTooManyRequests, "This stream's rate limit is reached. Try again later.")
		return
	}
	ctx, cancel := context.WithTimeout(c, ingestTimeout)
	defer cancel()
	results, err := eventstreams.Ingest(ctx, c.App.DB, s.id, s.test, events)
	if errors.Is(err, eventstreams.ErrNoStream) {
		writeStreamError(w, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		unavailable(w, "storing events", err)
		return
	}
	if !s.test {
		WakeRules(c, c.App.DB, s.id)
	}
	stored, dups := 0, 0
	for _, res := range results {
		if res.Duplicate {
			dups++
		} else {
			stored++
		}
	}
	streamReceived.With(stream).Add(float64(stored))
	streamDuplicates.With(stream).Add(float64(dups))
	api.WriteJSON(w, http.StatusAccepted, Obj{"received": len(results), "stored": stored, "duplicates": dups, "events": results})
}

func writeEventsError(w http.ResponseWriter, err error) {
	var e *eventstreams.Error
	if errors.As(err, &e) {
		writeStreamError(w, e.Status, e.Message)
		return
	}
	unavailable(w, "receiving events", err)
}

func streamPolicy(c *Ctx) eventstreams.Policy {
	var proxies, addrs []string
	c.App.Settings.Decode(c, eventstreams.TrustedProxiesSetting, &proxies)
	c.App.Settings.Decode(c, eventstreams.LocalAddressesSetting, &addrs)
	return eventstreams.NewPolicy(proxies, c.App.Settings.String(c, eventstreams.ClientCertHeaderSetting), addrs)
}

// MaintainEventStreams creates the event log's coming partitions, on the
// leader, as job events' are.
func MaintainEventStreams(ctx context.Context, m *jobs.Manager) {
	eventstreams.MaintainPartitions(ctx, m.DB())
}

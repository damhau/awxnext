package eventstreams

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damhau/awxnext/internal/jsonpath"
)

// Request is what a sender posted to a stream.
type Request struct {
	Header http.Header
	Body   []byte
	// Source is the stream's own path: the source of events whose format
	// names none.
	Source string
	// Mapping is a generic stream's JSONPaths for its events' attributes.
	Mapping map[string]string
}

// MappingKeys are the attributes a generic stream's mapping may find in
// the payload.
var MappingKeys = []string{"id", "type", "source", "subject", "time"}

// Parse reads the events of a request to a stream of type streamType. A
// refusal is an *Error.
func Parse(streamType string, r Request) ([]Event, error) {
	var events []Event
	var err error
	switch streamType {
	case TypeCloudEvents:
		events, err = parseCloudEvents(r)
	case TypeGeneric:
		events, err = parseGeneric(r)
	case TypeAlertmanager, TypeGrafana:
		events, err = parseAlerts(r)
	case TypeGitHub:
		events, err = parseGitHub(r)
	case TypeGitLab:
		events, err = parseGitLab(r)
	case TypeServiceNow:
		events, err = parseServiceNow(r)
	default:
		return nil, badRequest("This stream's type %q is unknown.", streamType)
	}
	if err != nil {
		return nil, err
	}
	for i := range events {
		if err := events[i].check(); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// AddTraceContext gives events the W3C trace context of the request that
// carried them, as CloudEvents' distributed tracing extension does, unless
// they have their own.
func AddTraceContext(events []Event, h http.Header) {
	parent, state := h.Get("Traceparent"), h.Get("Tracestate")
	if parent == "" {
		return
	}
	for i := range events {
		if _, ok := events[i].Extensions["traceparent"]; ok {
			continue
		}
		if events[i].Extensions == nil {
			events[i].Extensions = map[string]any{}
		}
		events[i].Extensions["traceparent"] = parent
		if state != "" {
			events[i].Extensions["tracestate"] = state
		}
	}
}

// coreAttributes are CloudEvents' own attributes, which no extension is.
var coreAttributes = map[string]bool{"specversion": true, "id": true, "source": true, "type": true, "subject": true,
	"datacontenttype": true, "dataschema": true, "time": true, "data": true, "database64": true}

// secretHeaders are never kept on events: they carry senders' secrets.
var secretHeaders = map[string]bool{"Authorization": true, "Proxy-Authorization": true, "Cookie": true}

// HeaderAttribute is the extension a request header of name is kept as:
// its name in lowercase letters and digits, such as xrequestid for
// X-Request-Id.
func HeaderAttribute(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AddHeaders gives events the request's headers of names, as EDA's
// additional_data_headers: each as an extension (HeaderAttribute), unless
// it is a secret's or the event has the attribute already.
func AddHeaders(events []Event, h http.Header, names []string) {
	for _, n := range names {
		n = http.CanonicalHeaderKey(strings.TrimSpace(n))
		attr := HeaderAttribute(n)
		v := h.Values(n)
		if attr == "" || len(v) == 0 || secretHeaders[n] || coreAttributes[attr] {
			continue
		}
		for i := range events {
			if _, ok := events[i].Extensions[attr]; ok {
				continue
			}
			if events[i].Extensions == nil {
				events[i].Extensions = map[string]any{}
			}
			events[i].Extensions[attr] = strings.Join(v, ", ")
		}
	}
}

func mediaType(h http.Header) string {
	t, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(h.Get("Content-Type")))
	}
	return t
}

func isJSON(mt string) bool {
	return mt == "" || mt == "application/json" || mt == "text/json" || strings.HasSuffix(mt, "+json")
}

// data stores a payload of media type contentType as an event's data: JSON
// as it is, other text as a JSON string.
func data(contentType string, body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	if isJSON(strings.ToLower(mt)) {
		if !json.Valid(body) {
			if mt == "" && utf8.Valid(body) {
				return json.Marshal(string(body))
			}
			return nil, badRequest("The event's data is not valid JSON.")
		}
		return compact(body), nil
	}
	if !utf8.Valid(body) {
		return nil, unsupported("The event's data is neither JSON nor text: awxnext keeps only those.")
	}
	return json.Marshal(string(body))
}

func compact(b []byte) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

func decodeJSON(body []byte) (any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, badRequest("The request has no body.")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, badRequest("The request's body is not valid JSON.")
	}
	return v, nil
}

func marshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// text is a payload value as an attribute: strings as they are, numbers
// and booleans written out, nothing for the rest.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool, float64:
		return fmt.Sprint(x)
	}
	return ""
}

func field(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	return cur
}

func parseTime(s string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

var attributeName = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

func parseCloudEvents(r Request) ([]Event, error) {
	if r.Header.Get("Ce-Specversion") != "" {
		ev, err := binaryEvent(r)
		if err != nil {
			return nil, err
		}
		return []Event{ev}, nil
	}
	switch mediaType(r.Header) {
	case "application/cloudevents+json":
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &raw); err != nil {
			return nil, badRequest("The request's body is not a CloudEvent in JSON.")
		}
		ev, err := structuredEvent(raw)
		if err != nil {
			return nil, err
		}
		return []Event{ev}, nil
	case "application/cloudevents-batch+json":
		var batch []map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &batch); err != nil {
			return nil, badRequest("The request's body is not a batch of CloudEvents in JSON.")
		}
		events := make([]Event, 0, len(batch))
		for i, raw := range batch {
			ev, err := structuredEvent(raw)
			if err != nil {
				return nil, badRequest("Event %d of the batch: %s", i+1, err.Error())
			}
			events = append(events, ev)
		}
		return events, nil
	}
	return nil, unsupported("A CloudEvents stream takes events in binary mode (ce- headers), in structured mode (application/cloudevents+json) or in batch mode (application/cloudevents-batch+json).")
}

// binaryEvent reads an event in CloudEvents' binary mode: its attributes in
// ce- headers, its data the body.
func binaryEvent(r Request) (Event, error) {
	ev := Event{DataContentType: r.Header.Get("Content-Type")}
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "ce-") {
			continue
		}
		name := lk[3:]
		value, err := url.PathUnescape(r.Header.Get(k))
		if err != nil {
			return ev, badRequest("The header %s is not percent-encoded correctly.", k)
		}
		switch name {
		case "specversion":
			if value != "1.0" {
				return ev, badRequest("CloudEvents %s is not supported: send version 1.0.", value)
			}
		case "id":
			ev.ID = value
		case "source":
			ev.Source = value
		case "type":
			ev.Type = value
		case "subject":
			ev.Subject = value
		case "dataschema":
			ev.DataSchema = value
		case "time":
			if ev.Time = parseTime(value); ev.Time == nil {
				return ev, badRequest("The event's time %q is not an RFC 3339 time.", value)
			}
		default:
			if !attributeName.MatchString(name) {
				return ev, badRequest("%s is not a CloudEvents attribute: their names are lowercase letters and digits.", k)
			}
			if ev.Extensions == nil {
				ev.Extensions = map[string]any{}
			}
			ev.Extensions[name] = value
		}
	}
	var err error
	ev.Data, err = data(ev.DataContentType, r.Body)
	return ev, err
}

// structuredEvent reads an event in CloudEvents' JSON format.
func structuredEvent(raw map[string]json.RawMessage) (Event, error) {
	var ev Event
	str := func(name string) (string, error) {
		v, ok := raw[name]
		if !ok {
			return "", nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", badRequest("The event's %s is not a string.", name)
		}
		return s, nil
	}
	spec, err := str("specversion")
	if err != nil {
		return ev, err
	}
	if spec != "1.0" {
		return ev, badRequest("The event's specversion must be 1.0.")
	}
	for name, dst := range map[string]*string{"id": &ev.ID, "source": &ev.Source, "type": &ev.Type, "subject": &ev.Subject,
		"datacontenttype": &ev.DataContentType, "dataschema": &ev.DataSchema} {
		if *dst, err = str(name); err != nil {
			return ev, err
		}
	}
	if t, err := str("time"); err != nil {
		return ev, err
	} else if t != "" {
		if ev.Time = parseTime(t); ev.Time == nil {
			return ev, badRequest("The event's time %q is not an RFC 3339 time.", t)
		}
	}
	d, hasData := raw["data"]
	b64, hasB64 := raw["data_base64"]
	switch {
	case hasData && hasB64:
		return ev, badRequest("The event has both data and data_base64.")
	case hasB64:
		var s string
		if err := json.Unmarshal(b64, &s); err != nil {
			return ev, badRequest("The event's data_base64 is not a string.")
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return ev, badRequest("The event's data_base64 is not base64.")
		}
		if ev.Data, err = data(ev.DataContentType, b); err != nil {
			return ev, err
		}
	case hasData && string(d) != "null":
		ev.Data = compact(d)
	}
	for name, v := range raw {
		switch name {
		case "specversion", "id", "source", "type", "subject", "datacontenttype", "dataschema", "time", "data", "data_base64":
			continue
		}
		if !attributeName.MatchString(name) {
			return ev, badRequest("%s is not a CloudEvents attribute: their names are lowercase letters and digits.", name)
		}
		var x any
		json.Unmarshal(v, &x)
		switch x.(type) {
		case string, float64, bool:
		default:
			return ev, badRequest("The event's attribute %s is not a string, a number or a boolean.", name)
		}
		if ev.Extensions == nil {
			ev.Extensions = map[string]any{}
		}
		ev.Extensions[name] = x
	}
	return ev, nil
}

// parseGeneric makes one event of any JSON payload. Its ID is the
// request's idempotency key, or what the stream's mapping finds; without
// either, every request is a new event.
func parseGeneric(r Request) ([]Event, error) {
	if mt := mediaType(r.Header); !isJSON(mt) {
		return nil, unsupported("A generic stream takes JSON, not %s.", mt)
	}
	doc, err := decodeJSON(r.Body)
	if err != nil {
		return nil, err
	}
	ev := Event{
		ID:              firstHeader(r.Header, "Idempotency-Key", "X-Request-Id", "X-Delivery-Id"),
		Type:            firstHeader(r.Header, "X-Event-Type"),
		Source:          r.Source,
		DataContentType: "application/json",
		Data:            compact(r.Body),
	}
	for key, path := range r.Mapping {
		v, ok := jsonpath.Get(doc, path)
		if !ok {
			continue
		}
		s := text(v)
		if s == "" {
			continue
		}
		switch key {
		case "id":
			ev.ID = s
		case "type":
			ev.Type = s
		case "source":
			ev.Source = s
		case "subject":
			ev.Subject = s
		case "time":
			ev.Time = parseTime(s)
		}
	}
	if ev.ID == "" {
		ev.ID = randomID()
	}
	if ev.Type == "" {
		ev.Type = "generic"
	}
	return []Event{ev}, nil
}

// parseAlerts splits an Alertmanager or Grafana notification into one
// event per alert. An alert's ID is its fingerprint, start and status, so
// the notifications Alertmanager repeats for the same alert are duplicates.
func parseAlerts(r Request) ([]Event, error) {
	doc, err := decodeJSON(r.Body)
	if err != nil {
		return nil, err
	}
	m, _ := doc.(map[string]any)
	alerts, ok := m["alerts"].([]any)
	if !ok {
		return nil, badRequest("The request is not an alert notification: it has no alerts.")
	}
	source := text(m["externalURL"])
	if source == "" {
		source = r.Source
	}
	events := make([]Event, 0, len(alerts))
	for i, a := range alerts {
		alert, ok := a.(map[string]any)
		if !ok {
			return nil, badRequest("Alert %d is not an object.", i+1)
		}
		status := text(alert["status"])
		if status == "" {
			status = text(m["status"])
		}
		if status == "" {
			return nil, badRequest("Alert %d has no status.", i+1)
		}
		fingerprint := text(alert["fingerprint"])
		if fingerprint == "" {
			sum := sha256.Sum256(marshal(alert["labels"]))
			fingerprint = hex.EncodeToString(sum[:8])
		}
		starts := text(alert["startsAt"])
		ev := Event{
			ID:              fingerprint + "/" + starts + "/" + status,
			Source:          source,
			Type:            "alert." + status,
			Subject:         text(field(alert, "labels", "alertname")),
			Time:            parseTime(starts),
			DataContentType: "application/json",
		}
		if status == "resolved" {
			if t := parseTime(text(alert["endsAt"])); t != nil {
				ev.Time = t
			}
		}
		d := map[string]any{}
		for k, v := range alert {
			d[k] = v
		}
		for _, k := range []string{"receiver", "groupKey", "groupLabels", "commonLabels", "commonAnnotations", "externalURL"} {
			if _, set := d[k]; !set && m[k] != nil {
				d[k] = m[k]
			}
		}
		ev.Data = marshal(d)
		events = append(events, ev)
	}
	return events, nil
}

// webhookPayload is a webhook's JSON payload, or the payload field of a
// form, as GitHub sends with the form content type.
func webhookPayload(r Request) (map[string]any, json.RawMessage, error) {
	body := r.Body
	if mediaType(r.Header) == "application/x-www-form-urlencoded" {
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("payload") == "" {
			return nil, nil, badRequest("The form has no payload.")
		}
		body = []byte(form.Get("payload"))
	}
	doc, err := decodeJSON(body)
	if err != nil {
		return nil, nil, err
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, nil, badRequest("The payload is not a JSON object.")
	}
	return m, compact(body), nil
}

func parseGitHub(r Request) ([]Event, error) {
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if event == "" || delivery == "" {
		return nil, badRequest("A GitHub webhook has X-GitHub-Event and X-GitHub-Delivery headers.")
	}
	m, raw, err := webhookPayload(r)
	if err != nil {
		return nil, err
	}
	source := r.Source
	for _, p := range [][]string{{"repository", "html_url"}, {"organization", "html_url"}, {"enterprise", "html_url"}} {
		if s := text(field(m, p...)); s != "" {
			source = s
			break
		}
	}
	return []Event{{
		ID: delivery, Type: "github." + event, Source: source, Subject: text(m["ref"]),
		DataContentType: "application/json", Data: raw,
	}}, nil
}

func parseGitLab(r Request) ([]Event, error) {
	event := r.Header.Get("X-Gitlab-Event")
	id := firstHeader(r.Header, "Idempotency-Key", "X-Gitlab-Event-UUID")
	if event == "" || id == "" {
		return nil, badRequest("A GitLab webhook has an X-Gitlab-Event header, and an Idempotency-Key or X-Gitlab-Event-UUID header.")
	}
	m, raw, err := webhookPayload(r)
	if err != nil {
		return nil, err
	}
	source := text(field(m, "project", "web_url"))
	if source == "" {
		source = r.Source
	}
	kind := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(event)), " hook")
	return []Event{{
		ID: id, Type: "gitlab." + strings.ReplaceAll(kind, " ", "_"), Source: source, Subject: text(m["ref"]),
		DataContentType: "application/json", Data: raw,
	}}, nil
}

// parseServiceNow makes an event of each ServiceNow record posted: one
// record, a list of them, or a Table API answer ({"result": ...}). Fields
// read with display values ({"value": ..., "display_value": ...}) count by
// their value.
func parseServiceNow(r Request) ([]Event, error) {
	doc, err := decodeJSON(r.Body)
	if err != nil {
		return nil, err
	}
	if m, ok := doc.(map[string]any); ok && m["result"] != nil && m["sys_id"] == nil {
		doc = m["result"]
	}
	var records []any
	switch x := doc.(type) {
	case []any:
		records = x
	case map[string]any:
		records = []any{x}
	default:
		return nil, badRequest("The request is not a ServiceNow record.")
	}
	value := func(rec map[string]any, k string) string {
		v := rec[k]
		if o, ok := v.(map[string]any); ok {
			v = o["value"]
		}
		return text(v)
	}
	events := make([]Event, 0, len(records))
	for i, x := range records {
		rec, ok := x.(map[string]any)
		if !ok {
			return nil, badRequest("Record %d is not an object.", i+1)
		}
		sysID := value(rec, "sys_id")
		if sysID == "" {
			return nil, badRequest("Record %d has no sys_id.", i+1)
		}
		version := value(rec, "sys_mod_count")
		if version == "" {
			version = value(rec, "sys_updated_on")
		}
		table := value(rec, "sys_class_name")
		if table == "" {
			table = value(rec, "table")
		}
		if table == "" {
			table = "record"
		}
		source := value(rec, "instance")
		if source == "" {
			source = r.Source
		}
		events = append(events, Event{
			ID: sysID + "/" + version, Type: "servicenow." + table, Source: source, Subject: value(rec, "number"),
			Time: parseTime(value(rec, "sys_updated_on")), DataContentType: "application/json", Data: marshal(rec),
		})
	}
	return events, nil
}

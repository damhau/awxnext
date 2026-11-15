package rules

import (
	"bytes"
	"encoding/json"

	"github.com/damhau/awxnext/internal/eventstreams"
)

// The throttles of a rule's key, as ansible-rulebook's throttle has them.
const (
	// OnceWithin fires on a key's first event, then holds back the key's
	// events until the window has passed.
	OnceWithin = "once_within"
	// OnceAfter opens a window on the first event, keeps the first event
	// of each key until the window ends, and fires once with them all.
	OnceAfter = "once_after"
)

// Value is an event as expressions see it, as a CloudEvent's JSON format
// has it: its attributes and extensions, and its data.
func Value(ev eventstreams.Stored) map[string]any {
	m := make(map[string]any, len(ev.Extensions)+9)
	for k, v := range ev.Extensions {
		m[k] = plain(v)
	}
	m["id"] = ev.ID
	m["source"] = ev.Source
	m["type"] = ev.Type
	m["specversion"] = "1.0"
	m["subject"] = ev.Subject
	m["datacontenttype"] = ev.DataContentType
	m["dataschema"] = ev.DataSchema
	if ev.Time != nil {
		m["time"] = ev.Time.UTC()
	}
	m["data"] = Data(ev.Data)
	return m
}

// Data decodes an event's data, whole numbers as integers.
func Data(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return string(raw)
	}
	return plain(v)
}

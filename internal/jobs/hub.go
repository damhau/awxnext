package jobs

import (
	"encoding/json"
	"strings"
	"sync"
)

// Hub fans out job status changes and events to websocket subscribers on this
// node. Messages produced on other nodes arrive through PostgreSQL notifications.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]bool
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan []byte]bool{}} }

// Subscribe registers ch for a group (e.g. "jobs-status_changed", "job_events-42").
func (h *Hub) Subscribe(group string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[group] == nil {
		h.subs[group] = map[chan []byte]bool{}
	}
	h.subs[group][ch] = true
}

// Unsubscribe removes ch from all groups.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for g, set := range h.subs {
		delete(set, ch)
		if len(set) == 0 {
			delete(h.subs, g)
		}
	}
}

// Publish delivers msg to local subscribers without blocking slow consumers.
func (h *Hub) Publish(group string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[group] {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (h *Hub) hasSubscribers(group string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[group]) > 0
}

// eventGroup is the AWX websocket group for a job's events.
func eventGroup(jobType string, id int64) string {
	prefix := map[string]string{
		"job": "job_events", "project_update": "project_update_events", "inventory_update": "inventory_update_events",
		"ad_hoc_command": "ad_hoc_command_events", "system_job": "system_job_events", "workflow_job": "workflow_events",
	}[jobType]
	if prefix == "" {
		prefix = jobType + "_events"
	}
	return prefix + "-" + itoa(id)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// publishEvent sends an event to local subscribers. Events are not relayed
// across nodes through NOTIFY (payload limits); remote viewers poll the API.
func (h *Hub) publishEvent(row Row, e Event) {
	group := eventGroup(row.Str("type"), row.ID())
	if !h.hasSubscribers(group) {
		return
	}
	msg := map[string]any{
		"group_name": group, "type": row.Str("type") + "_event", "event": e.Event, "counter": e.Counter,
		"uuid": e.UUID, "parent_uuid": e.ParentUUID, "stdout": e.Stdout, "start_line": e.StartLine, "end_line": e.EndLine,
		"host_name": e.HostName, "failed": e.Failed, "changed": e.Changed, "task": e.Task, "play": e.Play, "role": e.Role,
		"created": e.Created, "event_data": e.EventData,
	}
	if row.Str("type") == "job" {
		msg["job"] = row.ID()
	} else {
		msg[row.Str("type")] = row.ID()
	}
	b, _ := json.Marshal(msg)
	h.Publish(group, b)
}

// fromNotification relays status messages published by other nodes.
func (h *Hub) fromNotification(channel, payload, self string) {
	node, body, ok := strings.Cut(payload, "|")
	if !ok || node == self {
		return
	}
	if channel == chStatus {
		h.Publish("jobs-status_changed", []byte(body))
	}
}

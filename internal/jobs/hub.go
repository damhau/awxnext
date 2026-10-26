package jobs

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// Hub fans out job status changes and events to websocket subscribers on this
// node. Messages produced on other nodes arrive through PostgreSQL notifications.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]bool
	// relayed tracks the last event counter relayed per group from other nodes.
	relayed sync.Map
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

// UnsubscribeGroup removes ch from one group.
func (h *Hub) UnsubscribeGroup(group string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.subs[group]; set != nil {
		delete(set, ch)
		if len(set) == 0 {
			delete(h.subs, group)
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

// publishEvent sends an event to local subscribers. Subscribers on other
// nodes receive it through relayEvents once the event is stored.
func (h *Hub) publishEvent(row Row, e Event) {
	h.sendEvent(row.Str("type"), row.ID(), e)
}

func (h *Hub) sendEvent(jobType string, jobID int64, e Event) {
	group := eventGroup(jobType, jobID)
	if !h.hasSubscribers(group) {
		return
	}
	prefix, _, _ := strings.Cut(group, "-")
	msg := map[string]any{
		"group_name": prefix, "type": jobType + "_event", "event": e.Event, "counter": e.Counter,
		"uuid": e.UUID, "parent_uuid": e.ParentUUID, "stdout": e.Stdout, "start_line": e.StartLine, "end_line": e.EndLine,
		"host_name": e.HostName, "host": e.HostID, "failed": e.Failed, "changed": e.Changed, "task": e.Task, "play": e.Play, "role": e.Role,
		"playbook": e.Playbook, "verbosity": e.Verbosity, "created": e.Created, "event_data": e.EventData,
	}
	if jobType == "job" {
		msg["job"] = jobID
	} else {
		msg[jobType] = jobID
	}
	b, _ := json.Marshal(msg)
	h.Publish(group, b)
}

// relayEvents forwards events stored by another node to local subscribers.
func (m *Manager) relayEvents(ctx context.Context, payload string) {
	parts := strings.Split(payload, "|")
	if len(parts) != 4 || parts[0] == m.Node {
		return
	}
	jobType := parts[1]
	jobID, err1 := strconv.ParseInt(parts[2], 10, 64)
	maxCounter, err2 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil {
		return
	}
	group := eventGroup(jobType, jobID)
	if !m.Hub.hasSubscribers(group) {
		m.Hub.relayed.Delete(group)
		return
	}
	last := 0
	if v, ok := m.Hub.relayed.Load(group); ok {
		last = v.(int)
	}
	if maxCounter <= last {
		return
	}
	rows, err := m.DB().Query(ctx, `SELECT counter, event, uuid, parent_uuid, created, stdout, start_line, end_line, verbosity,
		host_name, host_id, playbook, play, role, task, failed, changed, event_data
		FROM job_events WHERE job_id = $1 AND counter > $2 AND counter <= $3 ORDER BY counter`, jobID, last, maxCounter)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Counter, &e.Event, &e.UUID, &e.ParentUUID, &e.Created, &e.Stdout, &e.StartLine, &e.EndLine, &e.Verbosity,
			&e.HostName, &e.HostID, &e.Playbook, &e.Play, &e.Role, &e.Task, &e.Failed, &e.Changed, &e.EventData); err != nil {
			return
		}
		m.Hub.sendEvent(jobType, jobID, e)
	}
	m.Hub.relayed.Store(group, maxCounter)
}

// publishWorkflow forwards a status message of a workflow-spawned job to the
// workflow's "workflow_events" group.
func (h *Hub) publishWorkflow(status []byte) {
	var m map[string]any
	if json.Unmarshal(status, &m) != nil {
		return
	}
	wf, ok := m["workflow_job_id"].(float64)
	if !ok {
		return
	}
	group := "workflow_events-" + itoa(int64(wf))
	if !h.hasSubscribers(group) {
		return
	}
	m["group_name"] = "workflow_events"
	b, _ := json.Marshal(m)
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
		h.publishWorkflow([]byte(body))
	}
}

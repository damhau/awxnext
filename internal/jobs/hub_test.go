package jobs

import (
	"testing"
	"time"
)

// MAX_WEBSOCKET_EVENT_RATE: each client gets at most that many job events per
// second; status messages are not limited.
func TestHubThrottlesEventsPerClient(t *testing.T) {
	h := NewHub()
	rate := 5
	h.EventRate = func() int { return rate }
	slow, other := make(chan []byte, 100), make(chan []byte, 100)
	h.Subscribe("job_events-1", slow)
	h.Subscribe("job_events-1", other)
	h.Subscribe("jobs-status_changed", slow)

	for i := range 20 {
		h.sendEvent("job", 1, Event{Counter: i + 1})
	}
	if len(slow) != 5 || len(other) != 5 {
		t.Fatalf("each client should get 5 events this second, got %d and %d", len(slow), len(other))
	}
	for range 3 {
		h.Publish("jobs-status_changed", []byte(`{}`))
	}
	if len(slow) != 8 {
		t.Fatalf("status messages are not throttled: %d messages", len(slow))
	}

	// The budget renews every second.
	h.budgets[slow].start = time.Now().Add(-time.Second)
	h.sendEvent("job", 1, Event{Counter: 21})
	if len(slow) != 9 {
		t.Fatalf("a new second should allow events again: %d messages", len(slow))
	}

	// 0: no limit.
	rate = 0
	for i := range 50 {
		h.sendEvent("job", 1, Event{Counter: 100 + i})
	}
	if len(other) != 55 {
		t.Fatalf("no limit: %d events", len(other))
	}

	h.Unsubscribe(slow)
	if _, ok := h.budgets[slow]; ok {
		t.Fatal("an unsubscribed client keeps its budget")
	}
}

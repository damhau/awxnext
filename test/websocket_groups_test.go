package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// wsStream is a subscribed connection's messages: coder/websocket closes a
// connection whose read is canceled, so one goroutine reads them all.
type wsStream chan map[string]any

// wsWait reads messages until one matches.
func wsWait(t *testing.T, s wsStream, match func(m map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case m, ok := <-s:
			if !ok {
				t.Fatal("websocket closed")
			}
			if match(m) {
				return m
			}
		case <-deadline:
			t.Fatal("no matching websocket message")
		}
	}
}

// wsNone reports a message matching within d.
func wsNone(t *testing.T, s wsStream, d time.Duration, match func(m map[string]any) bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-s:
			if match(m) {
				t.Errorf("unexpected message: %v", m)
			}
		case <-deadline:
			return
		}
	}
}

func wsJoin(t *testing.T, user, pass string, groups map[string]any) (*websocket.Conn, wsStream) {
	t.Helper()
	conn, err := wsDial(t, basicHeader(user, pass))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	wsRead(t, conn, 5*time.Second)
	wsSend(t, conn, map[string]any{"groups": groups})
	if m := wsRead(t, conn, 5*time.Second); m["groups_current"] == nil {
		t.Fatalf("subscribe: %v", m)
	}
	s := make(wsStream, 256)
	go func() {
		defer close(s)
		for {
			var m map[string]any
			if err := wsjson.Read(context.Background(), conn, &m); err != nil {
				return
			}
			s <- m
		}
	}()
	return conn, s
}

// AWX's jobs-summary, schedules-changed and control-limit_reached_<user>
// groups deliver their messages.
func TestWebsocketGroups(t *testing.T) {
	f := newJobFixture(t)
	a := f.a

	// jobs-summary: once a job's events are all stored, with its last counter.
	_, conn := wsJoin(t, "admin", "admin", map[string]any{"jobs": []any{"summary"}})
	job := f.launch(t, a, f.template(t, "site.yml", nil), nil)
	m := wsWait(t, conn, func(m map[string]any) bool { return m["unified_job_id"] == float64(job.id()) })
	events := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?order_by=-counter&page_size=1", job.id())), 200).results()
	if m["group_name"] != "jobs" || len(events) == 0 || m["final_counter"] != events[0]["counter"] {
		t.Errorf("jobs-summary: %v (last event %v)", m, events)
	}
	if j := must(t, a.get(fmt.Sprintf("jobs/%d/", job.id())), 200).Body; j["event_processing_finished"] != true {
		t.Errorf("summary before event processing finished: %v", j["event_processing_finished"])
	}

	// schedules-changed: on create, when the next run moves, and when the
	// scheduler runs it.
	_, sconn := wsJoin(t, "admin", "admin", map[string]any{"schedules": []any{"changed"}})
	jt := f.template(t, "site.yml", nil)
	sid := must(t, a.post("schedules/", map[string]any{"name": uniq("s"), "unified_job_template": jt,
		"rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY;INTERVAL=1"}), 201).id()
	if m := wsWait(t, sconn, func(m map[string]any) bool { return m["id"] == float64(sid) }); m["group_name"] != "schedules" {
		t.Errorf("schedules-changed on create: %v", m)
	}
	must(t, a.patch(fmt.Sprintf("schedules/%d/", sid), map[string]any{"description": "no change to its runs"}), 200)
	wsNone(t, sconn, time.Second, func(m map[string]any) bool { return m["id"] == float64(sid) })
	must(t, a.patch(fmt.Sprintf("schedules/%d/", sid), map[string]any{"rrule": "DTSTART:20310101T000000Z RRULE:FREQ=DAILY;INTERVAL=1"}), 200)
	wsWait(t, sconn, func(m map[string]any) bool { return m["id"] == float64(sid) })
	due := must(t, a.post("schedules/", map[string]any{"name": uniq("due"), "unified_job_template": jt,
		"rrule": fmt.Sprintf("DTSTART:%s RRULE:FREQ=MINUTELY;INTERVAL=1", time.Now().UTC().Add(-time.Hour).Format("20060102T150405Z"))}), 201).id()
	wsWait(t, sconn, func(m map[string]any) bool { return m["id"] == float64(due) })
	app.DB.Exec(context.Background(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", due)
	wsWait(t, sconn, func(m map[string]any) bool { return m["id"] == float64(due) })
	must(t, a.delete(fmt.Sprintf("schedules/%d/", due)), 204)

	// control-limit_reached_<user>: a login beyond SESSIONS_PER_USER ends the
	// oldest session, and that user's tabs hear it; others' do not.
	must(t, a.patch("settings/authentication/", map[string]any{"SESSIONS_PER_USER": 1}), 200)
	t.Cleanup(func() { a.patch("settings/authentication/", map[string]any{"SESSIONS_PER_USER": -1}) })
	_, uid := newUser(t, a)
	name := must(t, a.get(fmt.Sprintf("users/%d/", uid)), 200).Body["username"].(string)
	_, cconn := wsJoin(t, name, "pw", map[string]any{"control": []any{fmt.Sprintf("limit_reached_%d", uid)}})
	adminID := int64(must(t, a.get("me/"), 200).results()[0]["id"].(float64))
	otherConn, other := wsJoin(t, "admin", "admin", map[string]any{"control": []any{fmt.Sprintf("limit_reached_%d", adminID)}})
	first := login(t, name, "pw")
	wsNone(t, cconn, 500*time.Millisecond, func(m map[string]any) bool { return m["group_name"] == "control" })
	login(t, name, "pw")
	if m := wsWait(t, cconn, func(m map[string]any) bool { return m["group_name"] == "control" }); m["reason"] != "limit_reached" {
		t.Errorf("limit reached: %v", m)
	}
	if meWith(t, first) != 401 {
		t.Error("the oldest session still works")
	}
	wsNone(t, other, 500*time.Millisecond, func(m map[string]any) bool { return m["group_name"] == "control" })
	// Another user's group cannot be joined.
	wsSend(t, otherConn, map[string]any{"groups": map[string]any{"control": []any{fmt.Sprintf("limit_reached_%d", uid)}}})
	if m := wsWait(t, other, func(m map[string]any) bool { return m["error"] != nil || m["groups_current"] != nil }); m["error"] == nil {
		t.Errorf("joined another user's control group: %v", m)
	}
}

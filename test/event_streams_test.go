package test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/jackc/pgx/v5/pgxpool"
)

type eventStream struct {
	id   int64
	path string
}

func streamCredential(t *testing.T, a *client, org int64, namespace string, inputs map[string]any) int64 {
	t.Helper()
	return must(t, a.post("credentials/", map[string]any{"name": uniq(namespace), "organization": org,
		"credential_type": credType(t, a, namespace), "inputs": inputs}), 201).id()
}

func newEventStream(t *testing.T, a *client, org, cred int64, typ string, extra map[string]any) eventStream {
	t.Helper()
	body := map[string]any{"name": uniq("stream"), "organization": org, "credential": cred, "stream_type": typ}
	for k, v := range extra {
		body[k] = v
	}
	r := must(t, a.post("event_streams/", body), 201)
	uuid := r.Body["uuid"].(string)
	if r.Body["receiver_url"] != app.Cfg.BaseURL+"/api/v2/event_streams/"+uuid+"/post/" {
		t.Fatalf("receiver_url: %v", r.Body["receiver_url"])
	}
	return eventStream{id: r.id(), path: "event_streams/" + uuid + "/post/"}
}

func (s eventStream) post(t *testing.T, body string, headers map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	return postRaw(t, s.path, []byte(body), headers)
}

func (s eventStream) events(t *testing.T, a *client) []map[string]any {
	t.Helper()
	return must(t, a.get(fmt.Sprintf("event_streams/%d/events/?page_size=200", s.id)), 200).results()
}

func headers(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func sameTime(t *testing.T, got any, want string) {
	t.Helper()
	g, err1 := time.Parse(time.RFC3339Nano, fmt.Sprint(got))
	w, err2 := time.Parse(time.RFC3339Nano, want)
	if err1 != nil || err2 != nil || !g.Equal(w) {
		t.Errorf("time %v, want %s", got, want)
	}
}

func setSystemSetting(t *testing.T, a *client, key string, value, reset any) {
	t.Helper()
	must(t, a.patch("settings/system/", map[string]any{key: value}), 200)
	t.Cleanup(func() { a.patch("settings/system/", map[string]any{key: reset}) })
}

// Each authentication method takes its sender and refuses the others.
func TestEventStreamAuth(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	body := `{"msg":"hello"}`
	mac := func(h func() []byte) string { return base64.StdEncoding.EncodeToString(h()) }
	sha512Of := func(key string) func() []byte {
		return func() []byte { m := hmac.New(sha512.New, []byte(key)); m.Write([]byte(body)); return m.Sum(nil) }
	}
	sha256Of := func(key string) func() []byte {
		return func() []byte { m := hmac.New(sha256.New, []byte(key)); m.Write([]byte(body)); return m.Sum(nil) }
	}
	basic := func(u, p string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p)) }
	cases := []struct {
		name, namespace string
		inputs          map[string]any
		good, bad       map[string]string
	}{
		{"hmac", eventstreams.CredHMAC, map[string]any{"secret": "s3cret"},
			headers("X-Hub-Signature-256", sign("s3cret", []byte(body))), headers("X-Hub-Signature-256", sign("other", []byte(body)))},
		{"hmac sha512 base64", eventstreams.CredHMAC, map[string]any{"secret": "k", "hash_algorithm": "sha512", "http_header_key": "X-Signature",
			"signature_encoding": "base64", "signature_prefix": ""},
			headers("X-Signature", mac(sha512Of("k"))), headers("X-Signature", mac(sha256Of("k")))},
		{"github", eventstreams.CredGitHub, map[string]any{"secret": "gh"},
			headers("X-Hub-Signature-256", sign("gh", []byte(body))), headers("X-Hub-Signature-256", "sha256=00")},
		{"gitlab", eventstreams.CredGitLab, map[string]any{"token": "gl"}, headers("X-Gitlab-Token", "gl"), headers("X-Gitlab-Token", "nope")},
		{"token", eventstreams.CredToken, map[string]any{"token": "tok"}, headers("Authorization", "Bearer tok"), headers("Authorization", "Bearer tok2")},
		{"token header", eventstreams.CredToken, map[string]any{"token": "tok", "http_header_key": "X-Api-Key"},
			headers("X-Api-Key", "tok"), headers("Authorization", "Bearer tok")},
		{"basic", eventstreams.CredBasic, map[string]any{"username": "sender", "password": "pw"},
			headers("Authorization", basic("sender", "pw")), headers("Authorization", basic("sender", "no"))},
	}
	var tokenStream eventStream
	var tokenCred int64
	for _, tc := range cases {
		cred := streamCredential(t, a, org, tc.namespace, tc.inputs)
		s := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
		if tc.name == "token" {
			tokenStream, tokenCred = s, cred
		}
		tc.good["Content-Type"], tc.bad["Content-Type"] = "application/json", "application/json"
		if code, b, _ := s.post(t, body, tc.good); code != 202 || b["stored"] != float64(1) {
			t.Errorf("%s: %d %v", tc.name, code, b)
		}
		if code, b, _ := s.post(t, body, tc.bad); code != 401 {
			t.Errorf("%s, wrong credentials: %d %v", tc.name, code, b)
		}
		if code, _, _ := s.post(t, body, headers("Content-Type", "application/json")); code != 401 {
			t.Errorf("%s, no credentials: %d", tc.name, code)
		}
	}
	// The receiver never takes a user's own credentials.
	if code, _, _ := tokenStream.post(t, body, headers("Authorization", basic("admin", "admin"))); code != 401 {
		t.Errorf("a user's password: %d", code)
	}
	// Secrets are encrypted as every credential's, and neither they nor the
	// headers that carry them are stored with the events.
	var stored string
	app.DB.QueryRow(t.Context(), `SELECT inputs->>'token' FROM credentials WHERE id = $1`, tokenCred).Scan(&stored)
	if !crypto.IsEncrypted(stored) {
		t.Errorf("token stored as %q", stored)
	}
	var leaked int
	app.DB.QueryRow(t.Context(), `SELECT count(*) FROM stream_events WHERE stream_id = $1 AND (extensions::text LIKE '%tok%' OR data::text LIKE '%tok%')`,
		tokenStream.id).Scan(&leaked)
	if leaked != 0 {
		t.Errorf("credentials stored with %d events", leaked)
	}

	// A stream takes only an event stream credential, which its creator may use.
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": org, "credential_type": credType(t, a, "ssh"), "inputs": map[string]any{}}), 201).id()
	must(t, a.post("event_streams/", map[string]any{"name": uniq("s"), "organization": org, "credential": ssh}), 400)
	must(t, a.post("event_streams/", map[string]any{"name": uniq("s"), "organization": org}), 400)
	other := newOrg(t, a)
	user, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Admin"), "user": uid, "object_id": other}), 201)
	must(t, user.post("event_streams/", map[string]any{"name": uniq("s"), "organization": other, "credential": tokenCred}), 403)
	mine := streamCredential(t, user, other, eventstreams.CredToken, map[string]any{"token": "t"})
	s := must(t, user.post("event_streams/", map[string]any{"name": uniq("s"), "organization": other, "credential": mine}), 201)
	if r := must(t, user.get("event_streams/"), 200); r.count() != 1 {
		t.Errorf("the user's streams: %s", r.Raw)
	}
	must(t, user.patch(fmt.Sprintf("event_streams/%d/", s.id()), map[string]any{"credential": tokenCred}), 403)
	must(t, user.get(fmt.Sprintf("event_streams/%d/", tokenStream.id)), 403)
	if r := must(t, user.get("stream_events/"), 200); r.count() != 0 {
		t.Errorf("events of others' streams: %s", r.Raw)
	}
}

func clientDER(t *testing.T, ca *testCA, name string) []byte {
	t.Helper()
	_, _, pair := ca.issue(t, name, false)
	return pair.Certificate[0]
}

func pemOf(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func xfcc(der []byte) string {
	return `By=spiffe://cluster.local/ns/awx/sa/awxnext;Hash=00ff;Cert="` + url.PathEscape(pemOf(der)) + `";Subject="CN=x"`
}

// mTLS: the client certificate a trusted proxy passes on, in the formats
// proxies use; the header from anyone else is ignored.
func TestEventStreamMTLS(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ca, rogue := newTestCA(t), newTestCA(t)
	good, forged := clientDER(t, ca, "sender.example.com"), clientDER(t, rogue, "sender.example.com")
	cred := streamCredential(t, a, org, eventstreams.CredMTLS, map[string]any{"ca_certificate": ca.pem, "subject": "sender.example.com"})
	s := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
	post := func(h map[string]string) int {
		t.Helper()
		h["Content-Type"] = "application/json"
		code, _, _ := s.post(t, `{"x":1}`, h)
		return code
	}
	must(t, a.post("credentials/", map[string]any{"name": uniq("m"), "organization": org, "credential_type": credType(t, a, eventstreams.CredMTLS),
		"inputs": map[string]any{"ca_certificate": "not a certificate"}}), 400)

	if code := post(headers("X-Forwarded-Client-Cert", xfcc(good))); code != 401 {
		t.Fatalf("header believed without a trusted proxy: %d", code)
	}
	must(t, a.patch("settings/system/", map[string]any{"EVENT_STREAM_TRUSTED_PROXIES": []any{"not an address"}}), 400)
	setSystemSetting(t, a, "EVENT_STREAM_TRUSTED_PROXIES", []any{"127.0.0.0/8", "::1"}, []any{})
	for name, tc := range map[string]struct {
		header string
		want   int
	}{
		"envoy":                 {xfcc(good), 202},
		"appended after forged": {xfcc(forged) + "," + xfcc(good), 202},
		"forged appended":       {xfcc(good) + "," + xfcc(forged), 401},
		"other CA":              {xfcc(forged), 401},
		"not a certificate":     {"Cert=\"garbage\"", 401},
	} {
		if code := post(headers("X-Forwarded-Client-Cert", tc.header)); code != tc.want {
			t.Errorf("%s: %d, want %d", name, code, tc.want)
		}
	}
	if code := post(headers()); code != 401 {
		t.Errorf("no certificate: %d", code)
	}
	setSystemSetting(t, a, "EVENT_STREAM_CLIENT_CERT_HEADER", "Ssl-Client-Cert", "X-Forwarded-Client-Cert")
	if code := post(headers("Ssl-Client-Cert", url.QueryEscape(pemOf(good)))); code != 202 {
		t.Errorf("ingress-nginx: %d", code)
	}
	if code := post(headers("Ssl-Client-Cert", base64.StdEncoding.EncodeToString(good))); code != 202 {
		t.Errorf("traefik: %d", code)
	}
	must(t, a.patch(fmt.Sprintf("credentials/%d/", cred), map[string]any{"inputs": map[string]any{"ca_certificate": ca.pem, "subject": "other.example.com"}}), 200)
	if code := post(headers("Ssl-Client-Cert", url.QueryEscape(pemOf(good)))); code != 401 {
		t.Errorf("another subject: %d", code)
	}
}

// OIDC: tokens signed by the issuer, for the audience, whose claims meet
// the condition. The issuer is reached through netguard.
func TestEventStreamOIDC(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	idp := newFakeIdP(t)
	inputs := map[string]any{"issuer": idp.srv.URL, "audience": "awxnext-events", "claims": `claims.repository ==`}
	must(t, a.post("credentials/", map[string]any{"name": uniq("o"), "organization": org, "credential_type": credType(t, a, eventstreams.CredOIDC), "inputs": inputs}), 400)
	inputs["claims"] = `claims.repository == "org/app" && claims.ref.startsWith("refs/heads/")`
	cred := streamCredential(t, a, org, eventstreams.CredOIDC, inputs)
	s := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
	claims := map[string]any{"sub": "repo:org/app:ref:refs/heads/main", "repository": "org/app", "ref": "refs/heads/main"}
	post := func(token string) int {
		t.Helper()
		code, _, _ := s.post(t, `{"x":1}`, headers("Content-Type", "application/json", "Authorization", "Bearer "+token))
		return code
	}
	if code := post(idp.sign(t, claims, "awxnext-events")); code != 503 {
		t.Fatalf("an issuer on loopback, not allowed: %d", code)
	}
	setSystemSetting(t, a, "EVENT_STREAM_ALLOWED_LOCAL_ADDRESSES", []any{"127.0.0.1", "::1"}, []any{})
	if code := post(idp.sign(t, claims, "awxnext-events")); code != 202 {
		t.Fatalf("valid token: %d", code)
	}
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	other := map[string]any{"repository": "org/other", "ref": "refs/heads/main"}
	for name, tc := range map[string]struct {
		token string
		want  int
	}{
		"another audience": {idp.sign(t, claims, "someone-else"), 401},
		"another key":      {idp.signWith(t, otherKey, claims, "awxnext-events", time.Now().Add(time.Minute)), 401},
		"expired":          {idp.signWith(t, idp.key, claims, "awxnext-events", time.Now().Add(-time.Minute)), 401},
		"not a token":      {"abc", 401},
		"other claims":     {idp.sign(t, other, "awxnext-events"), 403},
		"missing claim":    {idp.sign(t, map[string]any{"repository": "org/app"}, "awxnext-events"), 403},
	} {
		if code := post(tc.token); code != tc.want {
			t.Errorf("%s: %d, want %d", name, code, tc.want)
		}
	}
}

// Every format becomes CloudEvents: id, source, type, subject, time, data.
func TestEventStreamFormats(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	h := func(kv ...string) map[string]string {
		m := headers(kv...)
		m["Authorization"] = "Bearer tok"
		if m["Content-Type"] == "" {
			m["Content-Type"] = "application/json"
		}
		return m
	}
	accept := func(s eventStream, body string, hd map[string]string, stored, dups int) {
		t.Helper()
		code, b, _ := s.post(t, body, hd)
		if code != 202 || b["stored"] != float64(stored) || b["duplicates"] != float64(dups) {
			t.Fatalf("%d %v", code, b)
		}
	}
	refuse := func(s eventStream, body string, hd map[string]string, want int) {
		t.Helper()
		if code, b, _ := s.post(t, body, hd); code != want {
			t.Errorf("%d, want %d: %v", code, want, b)
		}
	}
	check := func(ev map[string]any, want map[string]any) {
		t.Helper()
		for k, v := range want {
			got, _ := json.Marshal(ev[k])
			exp, _ := json.Marshal(v)
			if string(got) != string(exp) {
				t.Errorf("%s: %s, want %s (event %v)", k, got, exp, ev["event_id"])
			}
		}
	}

	ce := newEventStream(t, a, org, cred, eventstreams.TypeCloudEvents, nil)
	accept(ce, `{"disk": 91}`, h("Ce-Specversion", "1.0", "Ce-Id", "b-1", "Ce-Source", "/monitor/web-01", "Ce-Type", "com.example.disk.full",
		"Ce-Subject", "web-01", "Ce-Time", "2026-11-15T10:00:00Z", "Ce-Region", "eu%20west",
		"Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"), 1, 0)
	accept(ce, `{"specversion":"1.0","id":"s-1","source":"/monitor","type":"com.example.cpu","datacontenttype":"application/json","data":{"cpu":99},"team":"ops"}`,
		h("Content-Type", "application/cloudevents+json"), 1, 0)
	accept(ce, `[{"specversion":"1.0","id":"x-1","source":"/b","type":"t","datacontenttype":"text/plain","data_base64":"aGVsbG8="},
		{"specversion":"1.0","id":"x-2","source":"/b","type":"t","datacontenttype":"text/plain","data":"plain"}]`,
		h("Content-Type", "application/cloudevents-batch+json"), 2, 0)
	refuse(ce, `{"x":1}`, h(), 415)
	refuse(ce, `{"specversion":"1.0","id":"s-2","source":"/monitor"}`, h("Content-Type", "application/cloudevents+json"), 400)
	refuse(ce, `{"specversion":"0.3","id":"s-2","source":"/monitor","type":"t"}`, h("Content-Type", "application/cloudevents+json"), 400)
	refuse(ce, `[{"specversion":"1.0","id":"y-1","source":"/b","type":"t"},{"id":"y-2"}]`, h("Content-Type", "application/cloudevents-batch+json"), 400)
	evs := ce.events(t, a)
	if len(evs) != 4 {
		t.Fatalf("cloudevents: %v", evs)
	}
	check(evs[0], map[string]any{"sequence": 1, "event_id": "b-1", "source": "/monitor/web-01", "event_type": "com.example.disk.full", "subject": "web-01",
		"data": map[string]any{"disk": 91}, "data_content_type": "application/json", "test_mode": false,
		"extensions": map[string]any{"region": "eu west", "traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}})
	sameTime(t, evs[0]["event_time"], "2026-11-15T10:00:00Z")
	check(evs[1], map[string]any{"sequence": 2, "event_id": "s-1", "data": map[string]any{"cpu": 99}, "extensions": map[string]any{"team": "ops"}})
	check(evs[2], map[string]any{"sequence": 3, "data": "hello"})
	check(evs[3], map[string]any{"sequence": 4, "data": "plain", "event_time": nil})

	gen := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, map[string]any{"mapping": map[string]any{
		"id": "$.alert.id", "type": "$.kind", "subject": "$.host", "time": "$.at"}})
	payload := `{"alert":{"id":"a-7"},"kind":"disk.full","host":"web-01","at":"2026-11-15T09:00:00Z"}`
	accept(gen, payload, h(), 1, 0)
	accept(gen, payload, h(), 0, 1)
	evs = gen.events(t, a)
	check(evs[0], map[string]any{"event_id": "a-7", "event_type": "disk.full", "subject": "web-01", "source": fmt.Sprintf("/api/v2/event_streams/%d/", gen.id),
		"data": map[string]any{"alert": map[string]any{"id": "a-7"}, "kind": "disk.full", "host": "web-01", "at": "2026-11-15T09:00:00Z"}})
	sameTime(t, evs[0]["event_time"], "2026-11-15T09:00:00Z")
	plain := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
	accept(plain, `{"a":1}`, h("Idempotency-Key", "k-1", "X-Event-Type", "build.done"), 1, 0)
	accept(plain, `{"a":1}`, h("Idempotency-Key", "k-1"), 0, 1)
	accept(plain, `{"a":1}`, h(), 1, 0)
	accept(plain, `{"a":1}`, h(), 1, 0)
	refuse(plain, `nope`, h(), 400)
	refuse(plain, `a=1`, h("Content-Type", "application/x-www-form-urlencoded"), 415)
	if evs := plain.events(t, a); len(evs) != 3 || evs[0]["event_type"] != "build.done" || evs[1]["event_type"] != "generic" {
		t.Errorf("generic: %v", evs)
	}
	must(t, a.post("event_streams/", map[string]any{"name": uniq("s"), "organization": org, "credential": cred, "mapping": map[string]any{"id": "alert.id"}}), 400)
	must(t, a.post("event_streams/", map[string]any{"name": uniq("s"), "organization": org, "credential": cred, "mapping": map[string]any{"colour": "$.c"}}), 400)

	// Alertmanager: one event per alert, its ID from fingerprint, start and
	// status, so the repeats of a group are duplicates.
	am := newEventStream(t, a, org, cred, eventstreams.TypeAlertmanager, nil)
	group := func(status string) string {
		return `{"version":"4","groupKey":"{}:{alertname=\"DiskFull\"}","status":"` + status + `","receiver":"awxnext","externalURL":"http://alertmanager:9093",
			"groupLabels":{"alertname":"DiskFull"},"commonLabels":{"alertname":"DiskFull"},"alerts":[
			{"status":"` + status + `","labels":{"alertname":"DiskFull","instance":"web-01"},"startsAt":"2026-11-15T08:00:00Z","endsAt":"2026-11-15T09:00:00Z","fingerprint":"f1"},
			{"status":"firing","labels":{"alertname":"DiskFull","instance":"web-02"},"startsAt":"2026-11-15T08:05:00Z","endsAt":"0001-01-01T00:00:00Z","fingerprint":"f2"}]}`
	}
	accept(am, group("firing"), h(), 2, 0)
	accept(am, group("firing"), h(), 0, 2)
	accept(am, group("resolved"), h(), 1, 1)
	refuse(am, `{"status":"firing"}`, h(), 400)
	evs = am.events(t, a)
	if len(evs) != 3 {
		t.Fatalf("alertmanager: %v", evs)
	}
	check(evs[0], map[string]any{"event_id": "f1/2026-11-15T08:00:00Z/firing", "event_type": "alert.firing", "subject": "DiskFull", "source": "http://alertmanager:9093"})
	sameTime(t, evs[0]["event_time"], "2026-11-15T08:00:00Z")
	if d := evs[0]["data"].(map[string]any); d["receiver"] != "awxnext" || d["labels"].(map[string]any)["instance"] != "web-01" || d["groupKey"] == nil {
		t.Errorf("alert data: %v", d)
	}
	check(evs[2], map[string]any{"event_id": "f1/2026-11-15T08:00:00Z/resolved", "event_type": "alert.resolved"})
	sameTime(t, evs[2]["event_time"], "2026-11-15T09:00:00Z")

	gr := newEventStream(t, a, org, cred, eventstreams.TypeGrafana, nil)
	accept(gr, `{"receiver":"awxnext","status":"firing","orgId":1,"externalURL":"https://grafana.example.com/","title":"[FIRING:1] HighCPU",
		"alerts":[{"status":"firing","labels":{"alertname":"HighCPU"},"startsAt":"2026-11-15T07:00:00Z","fingerprint":"g1","values":{"B":97.5},
		"dashboardURL":"https://grafana.example.com/d/x"}]}`, h(), 1, 0)
	evs = gr.events(t, a)
	check(evs[0], map[string]any{"event_id": "g1/2026-11-15T07:00:00Z/firing", "event_type": "alert.firing", "subject": "HighCPU", "source": "https://grafana.example.com/"})
	if evs[0]["data"].(map[string]any)["values"].(map[string]any)["B"] != 97.5 {
		t.Errorf("grafana data: %v", evs[0]["data"])
	}

	// GitHub and GitLab: type and ID from their headers.
	gh := newEventStream(t, a, org, cred, eventstreams.TypeGitHub, nil)
	push := `{"ref":"refs/heads/main","repository":{"html_url":"https://github.com/org/app"}}`
	accept(gh, push, h("X-GitHub-Event", "push", "X-GitHub-Delivery", "d-1"), 1, 0)
	accept(gh, "payload="+url.QueryEscape(push), h("Content-Type", "application/x-www-form-urlencoded", "X-GitHub-Event", "push", "X-GitHub-Delivery", "d-2"), 1, 0)
	accept(gh, push, h("X-GitHub-Event", "push", "X-GitHub-Delivery", "d-1"), 0, 1)
	refuse(gh, push, h("X-GitHub-Event", "push"), 400)
	evs = gh.events(t, a)
	check(evs[0], map[string]any{"event_id": "d-1", "event_type": "github.push", "source": "https://github.com/org/app", "subject": "refs/heads/main"})
	check(evs[1], map[string]any{"event_id": "d-2", "data": map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"html_url": "https://github.com/org/app"}}})

	gl := newEventStream(t, a, org, cred, eventstreams.TypeGitLab, nil)
	mr := `{"object_kind":"merge_request","project":{"web_url":"https://gitlab.com/org/app"}}`
	accept(gl, mr, h("X-Gitlab-Event", "Merge Request Hook", "X-Gitlab-Event-UUID", "u-1"), 1, 0)
	accept(gl, mr, h("X-Gitlab-Event", "Merge Request Hook", "X-Gitlab-Event-UUID", "u-2", "Idempotency-Key", "i-1"), 1, 0)
	refuse(gl, mr, h("X-Gitlab-Event", "Merge Request Hook"), 400)
	evs = gl.events(t, a)
	check(evs[0], map[string]any{"event_id": "u-1", "event_type": "gitlab.merge_request", "source": "https://gitlab.com/org/app"})
	check(evs[1], map[string]any{"event_id": "i-1"})

	// ServiceNow: records, with or without display values.
	sn := newEventStream(t, a, org, cred, eventstreams.TypeServiceNow, nil)
	accept(sn, `{"result":{"sys_id":{"value":"abc","display_value":"abc"},"number":{"value":"INC001","display_value":"INC001"},
		"sys_class_name":"incident","sys_mod_count":"3","sys_updated_on":"2026-11-15 08:00:00","short_description":"Disk full"}}`, h(), 1, 0)
	accept(sn, `[{"sys_id":"r1","sys_mod_count":"0","sys_class_name":"change_request","number":"CHG1"},{"sys_id":"r2","sys_updated_on":"2026-11-15 09:00:00"}]`, h(), 2, 0)
	refuse(sn, `{"number":"INC2"}`, h(), 400)
	evs = sn.events(t, a)
	check(evs[0], map[string]any{"event_id": "abc/3", "event_type": "servicenow.incident", "subject": "INC001"})
	sameTime(t, evs[0]["event_time"], "2026-11-15T08:00:00Z")
	check(evs[1], map[string]any{"event_id": "r1/0", "event_type": "servicenow.change_request", "subject": "CHG1"})
	check(evs[2], map[string]any{"event_id": "r2/2026-11-15 09:00:00", "event_type": "servicenow.record"})
}

// 202 means stored; duplicates are dropped; the payload and rate limits;
// test mode; consumers' positions; 503 without the database.
func TestEventStreamDelivery(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	h := func(kv ...string) map[string]string {
		return headers(append([]string{"Authorization", "Bearer tok", "Content-Type", "application/json"}, kv...)...)
	}
	ctx := t.Context()
	s := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, map[string]any{"max_payload_bytes": 1024, "rate_limit": 3})
	must(t, a.patch(fmt.Sprintf("event_streams/%d/", s.id), map[string]any{"max_payload_bytes": 10}), 400)
	must(t, a.patch(fmt.Sprintf("event_streams/%d/", s.id), map[string]any{"retention_days": 0}), 400)

	code, b, _ := s.post(t, `{"n":1}`, h("Idempotency-Key", "e-1"))
	if code != 202 {
		t.Fatalf("post: %d %v", code, b)
	}
	first := b["events"].([]any)[0].(map[string]any)
	var n int
	app.DB.QueryRow(ctx, `SELECT count(*) FROM stream_events WHERE stream_id = $1 AND event_id = 'e-1'`, s.id).Scan(&n)
	if n != 1 || first["sequence"] != float64(1) || first["duplicate"] != false {
		t.Fatalf("202 before the event is stored: %d rows, %v", n, first)
	}
	_, b, _ = s.post(t, `{"n":1}`, h("Idempotency-Key", "e-1"))
	if dup := b["events"].([]any)[0].(map[string]any); dup["duplicate"] != true || dup["sequence"] != float64(1) || b["duplicates"] != float64(1) {
		t.Fatalf("duplicate: %v", b)
	}
	app.DB.QueryRow(ctx, `SELECT count(*) FROM stream_events WHERE stream_id = $1`, s.id).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate stored: %d", n)
	}
	if code, _, _ := s.post(t, `{"x":"`+strings.Repeat("a", 2000)+`"}`, h()); code != 413 {
		t.Errorf("payload limit: %d", code)
	}
	if code, _, _ := s.post(t, `{"n":2}`, h("Idempotency-Key", "e-2")); code != 202 {
		t.Errorf("third event of the minute: %d", code)
	}
	code, _, hd := s.post(t, `{"n":3}`, h("Idempotency-Key", "e-3"))
	if retry, _ := strconv.Atoi(hd.Get("Retry-After")); code != 429 || retry < 1 {
		t.Errorf("rate limit: %d, Retry-After %q", code, hd.Get("Retry-After"))
	}
	if r := must(t, a.get(fmt.Sprintf("event_streams/%d/", s.id)), 200); r.Body["last_sequence"] != float64(2) || r.Body["last_event_at"] == nil {
		t.Errorf("stream: %v %v", r.Body["last_sequence"], r.Body["last_event_at"])
	}
	am := newEventStream(t, a, org, cred, eventstreams.TypeAlertmanager, map[string]any{"rate_limit": 2})
	alerts := `{"alerts":[{"status":"firing","fingerprint":"1"},{"status":"firing","fingerprint":"2"},{"status":"firing","fingerprint":"3"}]}`
	if code, _, _ := am.post(t, alerts, h()); code != 413 {
		t.Errorf("more events than the stream takes at once: %d", code)
	}

	// Test mode: the stream keeps and shows its events; rules don't see them.
	tm := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, map[string]any{"test_mode": true})
	tm.post(t, `{"n":1}`, h())
	if evs := tm.events(t, a); len(evs) != 1 || evs[0]["test_mode"] != true {
		t.Errorf("test mode events: %v", evs)
	}
	if got, err := eventstreams.Read(ctx, app.DB, tm.id, 0, 10); err != nil || len(got) != 0 {
		t.Errorf("rules see test events: %v %v", got, err)
	}
	must(t, a.patch(fmt.Sprintf("event_streams/%d/", tm.id), map[string]any{"test_mode": false}), 200)
	tm.post(t, `{"n":2}`, h())
	if got, _ := eventstreams.Read(ctx, app.DB, tm.id, 0, 10); len(got) != 1 || got[0].Sequence != 2 {
		t.Errorf("after test mode: %v", got)
	}

	// Each consumer reads from its own position.
	got, err := eventstreams.Read(ctx, app.DB, s.id, 0, 10)
	var data map[string]any
	if err == nil && len(got) == 2 {
		json.Unmarshal(got[1].Data, &data)
	}
	if err != nil || len(got) != 2 || got[0].ID != "e-1" || got[1].Sequence != 2 || data["n"] != float64(2) {
		t.Fatalf("read: %v %v", got, err)
	}
	if pos, _ := eventstreams.Position(ctx, app.DB, "rule-a", s.id); pos != 0 {
		t.Errorf("new consumer: %d", pos)
	}
	if err := eventstreams.SavePosition(ctx, app.DB, "rule-a", s.id, 1); err != nil {
		t.Fatal(err)
	}
	eventstreams.SavePosition(ctx, app.DB, "rule-b", s.id, 2)
	if pos, _ := eventstreams.Position(ctx, app.DB, "rule-a", s.id); pos != 1 {
		t.Errorf("rule-a: %d", pos)
	}
	if got, _ := eventstreams.Read(ctx, app.DB, s.id, 1, 10); len(got) != 1 || got[0].ID != "e-2" {
		t.Errorf("read after 1: %v", got)
	}

	if code, _, _ := postRaw(t, "event_streams/00000000-0000-0000-0000-000000000000/post/", []byte(`{}`), h()); code != 404 {
		t.Errorf("unknown stream: %d", code)
	}
	if code, _, _ := postRaw(t, "event_streams/1/post/", []byte(`{}`), h()); code != 404 {
		t.Errorf("by id: %d", code)
	}

	// Without the database nothing is stored: 503, try again.
	dead, err := pgxpool.New(context.Background(), "postgres://awx:x@127.0.0.1:1/awx?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	down := *app
	down.DB = dead
	ts := httptest.NewServer(down.Router())
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v2/"+s.path, strings.NewReader(`{"n":4}`))
	for k, v := range h() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 || res.Header.Get("Retry-After") == "" {
		t.Errorf("without the database: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
}

// The log keeps each stream's events for its retention: the cleanup job
// drops whole days past every stream's, then each stream's expired events.
func TestEventStreamRetention(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ctx := t.Context()
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	h := func(id string) map[string]string {
		return headers("Authorization", "Bearer tok", "Content-Type", "application/json", "Idempotency-Key", id)
	}
	short := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, map[string]any{"retention_days": 1})
	long := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, map[string]any{"retention_days": 30})
	gone := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
	for _, p := range []struct {
		s  eventStream
		id string
	}{{short, "old"}, {short, "new"}, {long, "old"}, {gone, "x"}} {
		if code, b, _ := p.s.post(t, `{}`, h(p.id)); code != 202 {
			t.Fatalf("post: %d %v", code, b)
		}
	}
	ids := []int64{short.id, long.id}
	for _, table := range []string{"stream_events", "event_stream_dedup"} {
		if _, err := app.DB.Exec(ctx, `UPDATE `+table+` SET received = now() - interval '3 days' WHERE stream_id = ANY($1) AND event_id = 'old'`, ids); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS stream_events_20010101 PARTITION OF stream_events FOR VALUES FROM ('2001-01-01T00:00:00Z') TO ('2001-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	app.DB.Exec(ctx, `INSERT INTO stream_events (stream_id, sequence, received, event_id, source, event_type) VALUES ($1, 99, '2001-01-01T12:00:00Z', 'ancient', '/x', 't')`, long.id)
	must(t, a.delete(fmt.Sprintf("event_streams/%d/", gone.id)), 204)

	count := func(s eventStream) int {
		var n int
		app.DB.QueryRow(ctx, `SELECT count(*) FROM stream_events WHERE stream_id = $1`, s.id).Scan(&n)
		return n
	}
	var sjt int64
	app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = 'cleanup_event_streams'`).Scan(&sjt)
	if r := must(t, a.get(fmt.Sprintf("system_job_templates/%d/schedules/", sjt)), 200); r.count() != 1 {
		t.Errorf("default schedule: %s", r.Raw)
	}
	launch := func(extra map[string]any) string {
		t.Helper()
		j := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", sjt), map[string]any{"extra_vars": extra}), 201)
		if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", j.id())); r.Body["status"] != "successful" {
			t.Fatalf("cleanup: %v", r.Body)
		}
		return a.get(fmt.Sprintf("system_jobs/%d/stdout/?format=txt", j.id())).Raw
	}
	if out := launch(map[string]any{"dry_run": true}); !strings.Contains(out, "stream_events_20010101 would be dropped") || !strings.Contains(out, "Events would delete") {
		t.Errorf("dry run output:\n%s", out)
	}
	if count(short) != 2 || count(long) != 2 || count(gone) != 1 {
		t.Fatalf("the dry run removed events: %d %d %d", count(short), count(long), count(gone))
	}
	launch(map[string]any{})
	if count(short) != 1 || count(long) != 1 || count(gone) != 0 {
		t.Errorf("after cleanup: short %d, long %d, deleted %d", count(short), count(long), count(gone))
	}
	var exists bool
	app.DB.QueryRow(ctx, `SELECT to_regclass('stream_events_20010101') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Error("the old day's partition is still there")
	}
	// The IDs go with the events: an expired one is new again.
	if _, b, _ := short.post(t, `{}`, h("old")); b["stored"] != float64(1) {
		t.Errorf("expired ID: %v", b)
	}
	if _, b, _ := long.post(t, `{}`, h("old")); b["duplicates"] != float64(1) {
		t.Errorf("kept ID: %v", b)
	}

	// The log's coming days have their partitions.
	eventstreams.MaintainPartitions(ctx, app.DB)
	tomorrow := "stream_events_" + time.Now().UTC().AddDate(0, 0, 1).Format("20060102")
	app.DB.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tomorrow).Scan(&exists)
	if !exists {
		t.Errorf("no partition %s", tomorrow)
	}
}

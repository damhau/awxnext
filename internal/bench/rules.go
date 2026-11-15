package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RulesOptions sizes a rules run.
type RulesOptions struct {
	// Events posted, Batch per request, Concurrency requests at a time.
	Events, Batch, Concurrency int
	// Keys is how many hosts the events are about: the rule acts once per
	// host and throttles the others.
	Keys    int
	Timeout time.Duration
	Log     func(format string, args ...any)
}

// RulesResult is a rules run's measures.
type RulesResult struct {
	Target  string `json:"target"`
	Version string `json:"version"`
	Events  int    `json:"events"`
	// Matched is how many events the rule's filter holds for, so many
	// decisions; Actions how many of those acted (one per key).
	Matched int `json:"matched"`
	Actions int `json:"actions"`
	// IngestSeconds is from the first post until all were accepted;
	// DecideSeconds until the rule decided the last event; Lag from the
	// last post accepted until then.
	IngestSeconds    float64 `json:"ingest_seconds"`
	IngestPerSecond  float64 `json:"ingest_per_second"`
	DecideSeconds    float64 `json:"decide_seconds"`
	DecidedPerSecond float64 `json:"decided_per_second"`
	LagSeconds       float64 `json:"lag_seconds"`
}

// Rules measures awxnext's event rules: it posts CloudEvents to a stream
// that a rule reads, with a CEL filter, a key and once_within, and that
// posts an event to another stream per key; and measures until the rule
// has decided every event.
func Rules(ctx context.Context, t Target, o RulesOptions) (*RulesResult, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Minute
	}
	o.Batch, o.Concurrency, o.Keys = max(o.Batch, 1), max(o.Concurrency, 1), max(o.Keys, 1)
	c := &client{t: t, http: &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: t.Insecure}, MaxIdleConnsPerHost: 64}}} //nolint:gosec // Target.Insecure
	res := &RulesResult{Target: t.URL, Events: o.Events}
	var ping struct {
		Version string `json:"version"`
	}
	if _, err := c.do(ctx, "GET", "ping/", nil, &ping); err != nil {
		return nil, err
	}
	res.Version = ping.Version
	name := fmt.Sprintf("bench-rules-%d", time.Now().Unix())
	org, err := c.create(ctx, "organizations/", map[string]any{"name": name})
	if err != nil {
		return nil, err
	}
	var types struct {
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
	}
	if _, err := c.do(ctx, "GET", "credential_types/?namespace=event_stream_token", nil, &types); err != nil {
		return nil, err
	}
	if len(types.Results) != 1 {
		return nil, fmt.Errorf("the API has no event stream token credential type: is it awxnext?")
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret)
	cred, err := c.create(ctx, "credentials/", map[string]any{"name": name, "organization": org, "credential_type": types.Results[0].ID,
		"inputs": map[string]any{"token": token}})
	if err != nil {
		return nil, err
	}
	stream := func(suffix string) (int64, string, error) {
		var s struct {
			ID   int64  `json:"id"`
			UUID string `json:"uuid"`
		}
		_, err := c.do(ctx, "POST", "event_streams/", map[string]any{"name": name + suffix, "organization": org, "credential": cred,
			"stream_type": "cloudevents", "rate_limit": 0}, &s)
		return s.ID, s.UUID, err
	}
	source, uuid, err := stream("-alerts")
	if err != nil {
		return nil, err
	}
	target, _, err := stream("-remediations")
	if err != nil {
		return nil, err
	}
	rule, err := c.create(ctx, "event_rules/", map[string]any{"name": name, "organization": org, "streams": []int64{source},
		"filter": `event.type == "bench.alert" && event.data.severity == "critical"`, "key": "event.data.host",
		"throttle": "once_within", "throttle_window": 3600, "action": "post_event", "target_stream": target, "mode": "auto",
		"event_type": `"bench.remediate"`, "event_data": `{"host": event.data.host, "n": event.data.n}`})
	if err != nil {
		return nil, err
	}

	// Every tenth round of the hosts is warnings, which the filter drops.
	warning := func(i int) bool { return (i/o.Keys)%10 == 9 }
	keys := map[int]bool{}
	for i := 0; i < o.Events; i++ {
		if !warning(i) {
			res.Matched++
			keys[i%o.Keys] = true
		}
	}
	res.Actions = len(keys)
	batches := make(chan int)
	errs := make(chan error, o.Concurrency)
	var wg sync.WaitGroup
	post := func(from int) error {
		var evs []map[string]any
		for i := from; i < min(from+o.Batch, o.Events); i++ {
			severity := "critical"
			if warning(i) {
				severity = "warning"
			}
			evs = append(evs, map[string]any{"specversion": "1.0", "id": fmt.Sprintf("%s-%d", name, i), "source": "/awxbench", "type": "bench.alert",
				"data": map[string]any{"host": fmt.Sprintf("host-%d", i%o.Keys), "severity": severity, "n": i}})
		}
		b, _ := json.Marshal(evs)
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.URL, "/")+"/api/v2/event_streams/"+uuid+"/post/", bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/cloudevents-batch+json")
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		if r.StatusCode != http.StatusAccepted {
			return fmt.Errorf("posting events: %d %s", r.StatusCode, strings.TrimSpace(string(raw)))
		}
		return nil
	}
	o.Log("posting %d events, %d per request, %d requests at a time", o.Events, o.Batch, o.Concurrency)
	start := time.Now()
	for range o.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for from := range batches {
				if err := post(from); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	failed := false
	for from := 0; from < o.Events && !failed; from += o.Batch {
		select {
		case batches <- from:
		case err := <-errs:
			errs <- err
			failed = true
		}
	}
	close(batches)
	wg.Wait()
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	ingested := time.Since(start)
	res.IngestSeconds = ingested.Seconds()
	res.IngestPerSecond = float64(o.Events) / res.IngestSeconds

	o.Log("waiting for the rule to decide %d events", res.Matched)
	var decided time.Duration
	if err := pollFor(ctx, o.Timeout, 100*time.Millisecond, func() (bool, error) {
		var page struct {
			Count int `json:"count"`
		}
		if _, err := c.do(ctx, "GET", fmt.Sprintf("event_rules/%d/decisions/?page_size=1", rule), nil, &page); err != nil {
			return false, err
		}
		decided = time.Since(start)
		return page.Count >= res.Matched, nil
	}); err != nil {
		return nil, err
	}
	res.DecideSeconds = decided.Seconds()
	res.DecidedPerSecond = float64(res.Matched) / res.DecideSeconds
	res.LagSeconds = (decided - ingested).Seconds()
	var posted struct {
		Count int `json:"count"`
	}
	if _, err := c.do(ctx, "GET", fmt.Sprintf("event_rules/%d/decisions/?outcome=posted&page_size=1", rule), nil, &posted); err != nil {
		return nil, err
	}
	if posted.Count != res.Actions {
		return nil, fmt.Errorf("the rule acted %d times, not once per key (%d)", posted.Count, res.Actions)
	}
	return res, nil
}

package bench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// MaskingResult is the event job measured twice: without secrets, then with
// a credential holding Secrets of them, the first printed every 100 events.
type MaskingResult struct {
	Target  string   `json:"target"`
	Version string   `json:"version"`
	Secrets int      `json:"secrets"`
	Plain   EventRun `json:"plain"`
	Masked  EventRun `json:"masked"`
}

// Masking measures what masking secrets in job output costs event
// ingestion. AWX doesn't mask them, so on AWX both runs do the same work.
func Masking(ctx context.Context, t Target, o Options, secrets int) (*MaskingResult, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Minute
	}
	secrets = max(secrets, 1)
	c := newClient(t)
	res := &MaskingResult{Target: t.URL, Secrets: secrets}
	var ping struct {
		Version string `json:"version"`
	}
	if _, err := c.do(ctx, "GET", "ping/", nil, &ping); err != nil {
		return nil, err
	}
	res.Version = ping.Version
	fx, err := setUp(ctx, c, o)
	if err != nil {
		return nil, err
	}
	var fields []any
	env := map[string]any{}
	inputs := map[string]any{}
	for i := range secrets {
		id := fmt.Sprintf("secret_%02d", i)
		fields = append(fields, map[string]any{"id": id, "label": id, "secret": true})
		env[fmt.Sprintf("BENCH_SECRET_%02d", i)] = "{{ " + id + " }}"
		b := make([]byte, 12)
		rand.Read(b)
		inputs[id] = hex.EncodeToString(b)
	}
	ct, err := c.create(ctx, "credential_types/", map[string]any{"name": fx.name + "-secrets", "kind": "cloud",
		"inputs": map[string]any{"fields": fields}, "injectors": map[string]any{"env": env, "extra_vars": map[string]any{"bench_secret": "{{ secret_00 }}"}}})
	if err != nil {
		return nil, err
	}
	cred, err := c.create(ctx, "credentials/", map[string]any{"name": fx.name + "-secrets", "organization": fx.org, "credential_type": ct, "inputs": inputs})
	if err != nil {
		return nil, err
	}
	plain, err := fx.template(ctx, c, "events.yml", map[string]any{"count": o.Events})
	if err != nil {
		return nil, err
	}
	fx.name += "-masked"
	masked, err := fx.template(ctx, c, "events.yml", map[string]any{"count": o.Events})
	if err != nil {
		return nil, err
	}
	if _, err := c.do(ctx, "POST", fmt.Sprintf("job_templates/%d/credentials/", masked), map[string]any{"id": cred}, nil); err != nil {
		return nil, err
	}
	o.Log("event job without secrets: %d events", o.Events)
	if res.Plain, err = eventJob(ctx, c, o, plain); err != nil {
		return nil, err
	}
	o.Log("event job with %d secrets: %d events", secrets, o.Events)
	if res.Masked, err = eventJob(ctx, c, o, masked); err != nil {
		return nil, err
	}
	return res, nil
}

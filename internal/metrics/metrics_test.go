package metrics

import (
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := &Registry{}
	c := r.NewCounter("req_total", "Requests.", "method", "code")
	c.With("GET", "200").Inc()
	c.With("GET", "200").Add(2)
	c.With("POST", `5"0`).Inc()
	g := r.NewGauge("running", "Running jobs.")
	g.With().Set(3)
	h := r.NewHistogram("latency_seconds", "Latency.", []float64{0.1, 1}, "route")
	h.With("/x").Observe(0.05)
	h.With("/x").Observe(0.5)
	r.NewGaugeFunc("up", "Up.", func() float64 { return 1 })
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE req_total counter",
		`req_total{method="GET",code="200"} 3`,
		`req_total{method="POST",code="5\"0"} 1`,
		"running 3",
		`latency_seconds_bucket{route="/x",le="0.1"} 1`,
		`latency_seconds_bucket{route="/x",le="1"} 2`,
		`latency_seconds_bucket{route="/x",le="+Inf"} 2`,
		`latency_seconds_sum{route="/x"} 0.55`,
		`latency_seconds_count{route="/x"} 2`,
		"up 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

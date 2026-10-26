// Package metrics is a small Prometheus registry (counters, gauges,
// histograms with labels) rendered in the text exposition format.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families []family
}

type family interface {
	write(w io.Writer)
}

// Default is the process-wide registry served at /metrics.
var Default = &Registry{}

func (r *Registry) add(f family) {
	r.mu.Lock()
	r.families = append(r.families, f)
	r.mu.Unlock()
}

// Write renders all metrics.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	fams := append([]family{}, r.families...)
	r.mu.Unlock()
	for _, f := range fams {
		f.write(w)
	}
}

type desc struct {
	name, help, typ string
	labels          []string
}

func (d desc) header(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", d.name, escapeHelp(d.help), d.name, d.typ)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

// Labels renders {k="v",...}; extra pairs are appended.
func Labels(names, values []string, extra ...string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	parts := make([]string, 0, len(names)+len(extra)/2)
	for i, n := range names {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, n, escapeValue(values[i])))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, extra[i], escapeValue(extra[i+1])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// FormatValue renders a sample value.
func FormatValue(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

type vec[T any] struct {
	desc
	mu     sync.Mutex
	series map[string]*T
	values map[string][]string
	newT   func() *T
}

func (v *vec[T]) with(values ...string) *T {
	if len(values) != len(v.labels) {
		panic(fmt.Sprintf("metric %s: expected %d label values, got %d", v.name, len(v.labels), len(values)))
	}
	key := strings.Join(values, "\x00")
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.series[key]
	if !ok {
		s = v.newT()
		v.series[key] = s
		v.values[key] = append([]string{}, values...)
	}
	return s
}

func (v *vec[T]) sorted() []string {
	v.mu.Lock()
	keys := make([]string, 0, len(v.series))
	for k := range v.series {
		keys = append(keys, k)
	}
	v.mu.Unlock()
	sort.Strings(keys)
	return keys
}

type atomicFloat struct{ bits atomic.Uint64 }

func (a *atomicFloat) add(d float64) {
	for {
		old := a.bits.Load()
		if a.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}
func (a *atomicFloat) set(v float64) { a.bits.Store(math.Float64bits(v)) }
func (a *atomicFloat) get() float64  { return math.Float64frombits(a.bits.Load()) }

// CounterVec is a labelled counter.
type CounterVec struct{ v *vec[atomicFloat] }

// Counter is one counter series.
type Counter struct{ f *atomicFloat }

func (c Counter) Inc()          { c.f.add(1) }
func (c Counter) Add(d float64) { c.f.add(d) }

// NewCounter registers a counter in r.
func (r *Registry) NewCounter(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{v: &vec[atomicFloat]{desc: desc{name, help, "counter", labels}, series: map[string]*atomicFloat{}, values: map[string][]string{},
		newT: func() *atomicFloat { return &atomicFloat{} }}}
	r.add(c)
	return c
}

func (c *CounterVec) With(values ...string) Counter { return Counter{c.v.with(values...)} }

func (c *CounterVec) write(w io.Writer) {
	c.v.header(w)
	for _, k := range c.v.sorted() {
		c.v.mu.Lock()
		s, vals := c.v.series[k], c.v.values[k]
		c.v.mu.Unlock()
		fmt.Fprintf(w, "%s%s %s\n", c.v.name, Labels(c.v.labels, vals), FormatValue(s.get()))
	}
}

// GaugeVec is a labelled gauge.
type GaugeVec struct{ v *vec[atomicFloat] }

// Gauge is one gauge series.
type Gauge struct{ f *atomicFloat }

func (g Gauge) Set(v float64) { g.f.set(v) }
func (g Gauge) Add(d float64) { g.f.add(d) }

// NewGauge registers a gauge in r.
func (r *Registry) NewGauge(name, help string, labels ...string) *GaugeVec {
	g := &GaugeVec{v: &vec[atomicFloat]{desc: desc{name, help, "gauge", labels}, series: map[string]*atomicFloat{}, values: map[string][]string{},
		newT: func() *atomicFloat { return &atomicFloat{} }}}
	r.add(g)
	return g
}

func (g *GaugeVec) With(values ...string) Gauge { return Gauge{g.v.with(values...)} }

func (g *GaugeVec) write(w io.Writer) {
	g.v.header(w)
	for _, k := range g.v.sorted() {
		g.v.mu.Lock()
		s, vals := g.v.series[k], g.v.values[k]
		g.v.mu.Unlock()
		fmt.Fprintf(w, "%s%s %s\n", g.v.name, Labels(g.v.labels, vals), FormatValue(s.get()))
	}
}

// GaugeFunc reports a value computed at scrape time.
type GaugeFunc struct {
	desc
	fn func() float64
}

// NewGaugeFunc registers a gauge whose value is computed on each scrape.
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) {
	r.add(&GaugeFunc{desc{name, help, "gauge", nil}, fn})
}

func (g *GaugeFunc) write(w io.Writer) {
	g.header(w)
	fmt.Fprintf(w, "%s %s\n", g.name, FormatValue(g.fn()))
}

type histogram struct {
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// HistogramVec is a labelled histogram.
type HistogramVec struct {
	v       *vec[histogram]
	buckets []float64
}

// Histogram is one histogram series.
type Histogram struct{ h *histogram }

// Observe records a value.
func (h Histogram) Observe(v float64) {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	for i, b := range h.h.buckets {
		if v <= b {
			h.h.counts[i]++
		}
	}
	h.h.sum += v
	h.h.count++
}

// DefaultBuckets suit request latencies in seconds.
var DefaultBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// NewHistogram registers a histogram in r.
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *HistogramVec {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	h := &HistogramVec{buckets: buckets}
	h.v = &vec[histogram]{desc: desc{name, help, "histogram", labels}, series: map[string]*histogram{}, values: map[string][]string{},
		newT: func() *histogram { return &histogram{buckets: buckets, counts: make([]uint64, len(buckets))} }}
	r.add(h)
	return h
}

func (h *HistogramVec) With(values ...string) Histogram { return Histogram{h.v.with(values...)} }

func (h *HistogramVec) write(w io.Writer) {
	h.v.header(w)
	for _, k := range h.v.sorted() {
		h.v.mu.Lock()
		s, vals := h.v.series[k], h.v.values[k]
		h.v.mu.Unlock()
		s.mu.Lock()
		for i, b := range s.buckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.v.name, Labels(h.v.labels, vals, "le", FormatValue(b)), s.counts[i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.v.name, Labels(h.v.labels, vals, "le", "+Inf"), s.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.v.name, Labels(h.v.labels, vals), FormatValue(s.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.v.name, Labels(h.v.labels, vals), s.count)
		s.mu.Unlock()
	}
}

// Writer collects ad-hoc samples (for metrics computed from the database).
type Writer struct {
	w    io.Writer
	seen map[string]bool
}

// NewWriter starts writing exposition text to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w, seen: map[string]bool{}} }

// Sample writes one sample, emitting HELP/TYPE the first time a name is used.
func (mw *Writer) Sample(name, typ, help string, value float64, labels ...string) {
	if !mw.seen[name] {
		mw.seen[name] = true
		desc{name: name, help: help, typ: typ}.header(mw.w)
	}
	fmt.Fprintf(mw.w, "%s%s %s\n", name, Labels(nil, nil, labels...), FormatValue(value))
}

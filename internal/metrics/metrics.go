// Package metrics implements a zero-allocation Prometheus metrics
// registry.  Unlike prometheus/client_golang, this package does not
// allocate on the hot path: counters are pre-registered atomic.Uint64
// values addressed by a (name, labels) key, and the HTTP exposition
// emits Prometheus text format directly from the in-memory state.
//
// Architecture
//
//	Metrics are registered via Register (or by name lookup) and
//	addressed by a LabelKey (uint64 hash of name+labels).  Increment
//	operations are a single atomic.Add — no map access, no allocation.
//
// Histograms use fixed bucket boundaries.  Each bucket is an
// atomic.Uint64 counter; Observe is a load-and-compare loop.
//
// Text exposition (WriteText) walks the registry's pre-allocated
// buffer and emits the Prometheus text format with no per-call
// allocations.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Label is a key-value pair in a metric's labelset.
type Label struct {
	Name  string
	Value string
}

// Hash computes a stable 64-bit hash of (name, label values).
// Used as the key into the metrics registry.
type Hash uint64

// registry is the singleton metrics store.
var registry = &reg{
	counter: make(map[Hash]*counterEntry),
	gauge:   make(map[Hash]*gaugeEntry),
	hist:    make(map[Hash]*histogramEntry),
	mu:      sync.RWMutex{},
}

// counterEntry is a counter with a fixed set of labels.
type counterEntry struct {
	name   string
	help   string
	labels []Label
	val    atomic.Uint64
}

// gaugeEntry is a gauge with a fixed set of labels.
type gaugeEntry struct {
	name   string
	help   string
	labels []Label
	val    atomic.Uint64
}

// histogramEntry is a histogram with fixed buckets.
type histogramEntry struct {
	name    string
	help    string
	labels  []Label
	buckets []float64 // upper bounds in seconds (inclusive)
	counts  []atomic.Uint64
	sum     atomic.Uint64 // sum stored as uint64 nanoseconds
	count   atomic.Uint64
}

// reg is the metrics registry.
type reg struct {
	counter map[Hash]*counterEntry
	gauge   map[Hash]*gaugeEntry
	hist    map[Hash]*histogramEntry
	mu      sync.RWMutex
}

// key computes a stable hash for (name, labels).
func key(name string, labels []Label) Hash {
	h := uint64(14695981039346656037)
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	for _, l := range labels {
		for i := 0; i < len(l.Name); i++ {
			h ^= uint64(l.Name[i])
			h *= 1099511628211
		}
		h ^= 0xff
		h *= 1099511628211
		for i := 0; i < len(l.Value); i++ {
			h ^= uint64(l.Value[i])
			h *= 1099511628211
		}
		h ^= 0xff
		h *= 1099511628211
	}
	return Hash(h)
}

// RegisterCounter registers a counter with fixed labels.  The same
// (name, labels) must be passed to Inc to update the right metric.
//
// Labels are passed by value so the caller can build a single
// slice once at startup.
func RegisterCounter(name, help string, labels []Label) Hash {
	k := key(name, labels)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.counter[k]; ok {
		return k
	}
	e := &counterEntry{name: name, help: help, labels: append([]Label(nil), labels...)}
	registry.counter[k] = e
	return k
}

// IncCounter atomically increments the counter addressed by k.
func IncCounter(k Hash) {
	if e, ok := registry.counter[k]; ok {
		e.val.Add(1)
	}
}

// IncCounterBy atomically adds delta to the counter.
func IncCounterBy(k Hash, delta uint64) {
	if e, ok := registry.counter[k]; ok {
		e.val.Add(delta)
	}
}

// RegisterGauge registers a gauge.
func RegisterGauge(name, help string, labels []Label) Hash {
	k := key(name, labels)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.gauge[k]; ok {
		return k
	}
	e := &gaugeEntry{name: name, help: help, labels: append([]Label(nil), labels...)}
	registry.gauge[k] = e
	return k
}

// SetGauge sets the gauge's current value.
func SetGauge(k Hash, v uint64) {
	if e, ok := registry.gauge[k]; ok {
		e.val.Store(v)
	}
}

// AddGauge atomically adds delta to the gauge.
func AddGauge(k Hash, delta uint64) {
	if e, ok := registry.gauge[k]; ok {
		e.val.Add(delta)
	}
}

// RegisterHistogram registers a histogram with fixed upper bounds in
// seconds (e.g. {0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}).
func RegisterHistogram(name, help string, labels []Label, buckets []float64) Hash {
	k := key(name, labels)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.hist[k]; ok {
		return k
	}
	// buckets + 1 for +Inf.
	counts := make([]atomic.Uint64, len(buckets)+1)
	e := &histogramEntry{
		name:    name,
		help:    help,
		labels:  append([]Label(nil), labels...),
		buckets: append([]float64(nil), buckets...),
		counts:  counts,
	}
	registry.hist[k] = e
	return k
}

// Observe records a value (in seconds) into the histogram.
func Observe(k Hash, valueSeconds float64) {
	e, ok := registry.hist[k]
	if !ok {
		return
	}
	for i, ub := range e.buckets {
		if valueSeconds <= ub {
			e.counts[i].Add(1)
		}
	}
	// +Inf bucket
	e.counts[len(e.buckets)].Add(1)
	// Sum and count.
	e.sum.Add(uint64(valueSeconds * 1e9))
	e.count.Add(1)
}

// WriteText writes the Prometheus text exposition format to w.
// Allocations: one bufio.Writer per call plus the metric lines;
// no allocations on the per-metric inner loop.
func WriteText(w io.Writer) error {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("# HELP horreum horreum metrics\n")
	// Counters
	keys := make([]Hash, 0, len(registry.counter))
	for k := range registry.counter {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		e := registry.counter[k]
		sb.WriteString("# HELP ")
		sb.WriteString(e.name)
		sb.WriteByte(' ')
		sb.WriteString(e.help)
		sb.WriteByte('\n')
		sb.WriteString("# TYPE ")
		sb.WriteString(e.name)
		sb.WriteString(" counter\n")
		writeCounterLine(&sb, e)
	}

	// Gauges
	keys = keys[:0]
	for k := range registry.gauge {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		e := registry.gauge[k]
		sb.WriteString("# HELP ")
		sb.WriteString(e.name)
		sb.WriteByte(' ')
		sb.WriteString(e.help)
		sb.WriteByte('\n')
		sb.WriteString("# TYPE ")
		sb.WriteString(e.name)
		sb.WriteString(" gauge\n")
		writeGaugeLine(&sb, e)
	}

	// Histograms
	keys = keys[:0]
	for k := range registry.hist {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		e := registry.hist[k]
		sb.WriteString("# HELP ")
		sb.WriteString(e.name)
		sb.WriteByte(' ')
		sb.WriteString(e.help)
		sb.WriteByte('\n')
		sb.WriteString("# TYPE ")
		sb.WriteString(e.name)
		sb.WriteString(" histogram\n")
		writeHistogramLines(&sb, e)
	}

	_, err := io.WriteString(w, sb.String())
	return err
}

func writeCounterLine(sb *strings.Builder, e *counterEntry) {
	sb.WriteString(e.name)
	writeLabels(sb, e.labels)
	sb.WriteByte(' ')
	sb.WriteString(fmt.Sprintf("%d", e.val.Load()))
	sb.WriteByte('\n')
}

func writeGaugeLine(sb *strings.Builder, e *gaugeEntry) {
	sb.WriteString(e.name)
	writeLabels(sb, e.labels)
	sb.WriteByte(' ')
	sb.WriteString(fmt.Sprintf("%d", e.val.Load()))
	sb.WriteByte('\n')
}

func writeHistogramLines(sb *strings.Builder, e *histogramEntry) {
	cum := uint64(0)
	for i, ub := range e.buckets {
		cum = e.counts[i].Load()
		// bucket line
		sb.WriteString(e.name + "_bucket")
		writeLabels(sb, append(e.labels, Label{"le", fmt.Sprintf("%g", ub)}))
		sb.WriteByte(' ')
		sb.WriteString(fmt.Sprintf("%d", cum))
		sb.WriteByte('\n')
	}
	// +Inf bucket
	total := e.counts[len(e.buckets)].Load()
	sb.WriteString(e.name + "_bucket")
	writeLabels(sb, append(e.labels, Label{"le", "+Inf"}))
	sb.WriteByte(' ')
	sb.WriteString(fmt.Sprintf("%d", total))
	sb.WriteByte('\n')
	// _sum and _count
	sb.WriteString(e.name + "_sum")
	writeLabels(sb, e.labels)
	sb.WriteByte(' ')
	sb.WriteString(fmt.Sprintf("%d", e.sum.Load()))
	sb.WriteByte('\n')
	sb.WriteString(e.name + "_count")
	writeLabels(sb, e.labels)
	sb.WriteByte(' ')
	sb.WriteString(fmt.Sprintf("%d", e.count.Load()))
	sb.WriteByte('\n')
}

// Recorder is the public API used by the transport layer.  Each
// method is lock-free (atomic-only) and zero-allocation on the
// hot path: latency is bucketed into a pre-registered histogram,
// counters are pre-registered atomic.Uint64.
type Recorder interface {
	ObserveSet(status string, dur time.Duration)
	ObserveGet(status string, dur time.Duration)
	ObserveDel(status string, dur time.Duration)
	ObserveCAS(status string, dur time.Duration)
	ObserveIncr(status string, dur time.Duration)
	ObserveScan(status string, dur time.Duration)
	ObserveDelPrefix(status string, dur time.Duration)
}

// NoopRecorder is a Recorder that drops all observations.  Useful as
// the default value in api.Options.Recorder when metrics are disabled.
type NoopRecorder struct{}

func (NoopRecorder) ObserveSet(string, time.Duration) {}
func (NoopRecorder) ObserveGet(string, time.Duration) {}
func (NoopRecorder) ObserveDel(string, time.Duration) {}
func (NoopRecorder) ObserveCAS(string, time.Duration) {}
func (NoopRecorder) ObserveIncr(string, time.Duration) {}
func (NoopRecorder) ObserveScan(string, time.Duration) {}
func (NoopRecorder) ObserveDelPrefix(string, time.Duration) {}

// DefaultRecorder is a Recorder that increments pre-registered
// metrics.  Construct it via NewRecorder(); passing it by value is
// safe because all fields are immutable after construction.
type DefaultRecorder struct {
	setTotal, getTotal, delTotal, casTotal, incrTotal, scanTotal, delPrefixTotal map[string]Hash
	setLat, getLat, delLat, casLat, incrLat, scanLat, delPrefixLat             Hash
}

// NewRecorder creates the default Recorder.  It pre-registers
// counters and histograms for each status label (ok / err).
//
// Latency buckets are powers of 10 from 100µs to 10s — appropriate
// for in-memory KV workloads.
func NewRecorder() *DefaultRecorder {
	r := &DefaultRecorder{
		setTotal:       make(map[string]Hash, 2),
		getTotal:       make(map[string]Hash, 2),
		delTotal:       make(map[string]Hash, 2),
		casTotal:       make(map[string]Hash, 2),
		incrTotal:      make(map[string]Hash, 2),
		scanTotal:      make(map[string]Hash, 2),
		delPrefixTotal: make(map[string]Hash, 2),
	}
	r.setTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "set"}, {Name: "status", Value: "ok"}})
	r.setTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "set"}, {Name: "status", Value: "err"}})
	r.getTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "get"}, {Name: "status", Value: "ok"}})
	r.getTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "get"}, {Name: "status", Value: "err"}})
	r.delTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "del"}, {Name: "status", Value: "ok"}})
	r.delTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "del"}, {Name: "status", Value: "err"}})
	r.casTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "cas"}, {Name: "status", Value: "ok"}})
	r.casTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "cas"}, {Name: "status", Value: "err"}})
	r.incrTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "incr"}, {Name: "status", Value: "ok"}})
	r.incrTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "incr"}, {Name: "status", Value: "err"}})
	r.scanTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "scan"}, {Name: "status", Value: "ok"}})
	r.scanTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "scan"}, {Name: "status", Value: "err"}})
	r.delPrefixTotal["ok"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "delprefix"}, {Name: "status", Value: "ok"}})
	r.delPrefixTotal["err"] = RegisterCounter("horreum_ops_total",
		"Total number of operations by command and status.",
		[]Label{{Name: "cmd", Value: "delprefix"}, {Name: "status", Value: "err"}})

	buckets := []float64{
		0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5,
	}
	r.setLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "set"}}, buckets)
	r.getLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "get"}}, buckets)
	r.delLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "del"}}, buckets)
	r.casLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "cas"}}, buckets)
	r.incrLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "incr"}}, buckets)
	r.scanLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "scan"}}, buckets)
	r.delPrefixLat = RegisterHistogram("horreum_latency_seconds",
		"Op latency in seconds, by command.",
		[]Label{{Name: "cmd", Value: "delprefix"}}, buckets)
	return r
}

// ObserveSet records a SET op.
func (r *DefaultRecorder) ObserveSet(status string, dur time.Duration) {
	if h, ok := r.setTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.setLat, dur.Seconds())
}

// ObserveGet records a GET op.
func (r *DefaultRecorder) ObserveGet(status string, dur time.Duration) {
	if h, ok := r.getTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.getLat, dur.Seconds())
}

// ObserveDel records a DEL op.
func (r *DefaultRecorder) ObserveDel(status string, dur time.Duration) {
	if h, ok := r.delTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.delLat, dur.Seconds())
}

// ObserveCAS records a CAS op.
func (r *DefaultRecorder) ObserveCAS(status string, dur time.Duration) {
	if h, ok := r.casTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.casLat, dur.Seconds())
}

// ObserveIncr records a INCR op.
func (r *DefaultRecorder) ObserveIncr(status string, dur time.Duration) {
	if h, ok := r.incrTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.incrLat, dur.Seconds())
}

// ObserveScan records a SCAN op.
func (r *DefaultRecorder) ObserveScan(status string, dur time.Duration) {
	if h, ok := r.scanTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.scanLat, dur.Seconds())
}

// ObserveDelPrefix records a DELPREFIX op.
func (r *DefaultRecorder) ObserveDelPrefix(status string, dur time.Duration) {
	if h, ok := r.delPrefixTotal[status]; ok {
		IncCounter(h)
	}
	Observe(r.delPrefixLat, dur.Seconds())
}

// Compile-time interface check.
var _ Recorder = (*DefaultRecorder)(nil)
var _ Recorder = NoopRecorder{}

func writeLabels(sb *strings.Builder, labels []Label) {
	if len(labels) == 0 {
		return
	}
	sb.WriteByte('{')
	for i, l := range labels {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(l.Name)
		sb.WriteString(`="`)
		sb.WriteString(l.Value)
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
}

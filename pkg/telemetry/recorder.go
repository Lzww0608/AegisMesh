package telemetry

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type Observation struct {
	Source      string
	Destination string
	Method      string
	Upstream    string
	Status      string
	Latency     time.Duration
	Error       bool
	Timeout     bool
}

type EndpointStats struct {
	Source       string
	Destination  string
	Method       string
	Upstream     string
	RequestCount int64
	ErrorCount   int64
	TimeoutCount int64
	Inflight     int64
	LatencyEWMA  time.Duration
	LatencyP95   time.Duration
	WindowStart  time.Time
	WindowEnd    time.Time
}

type MetricsSink interface {
	Record(obs Observation, latencyEWMA time.Duration, inflight int64)
}

type Recorder struct {
	mu      sync.Mutex
	source  string
	metrics MetricsSink
	now     func() time.Time
	rows    map[statsKey]*statsRow
}

type statsKey struct {
	destination string
	method      string
	upstream    string
}

type statsRow struct {
	requestCount int64
	errorCount   int64
	timeoutCount int64
	inflight     int64
	ewma         *EWMA
	latencies    []time.Duration
	windowStart  time.Time
}

func NewRecorder(source string, metrics MetricsSink) *Recorder {
	return NewRecorderWithClock(source, metrics, time.Now)
}

func NewRecorderWithClock(source string, metrics MetricsSink, now func() time.Time) *Recorder {
	if source == "" {
		source = "unknown"
	}
	if now == nil {
		now = time.Now
	}
	return &Recorder{
		source:  source,
		metrics: metrics,
		now:     now,
		rows:    make(map[statsKey]*statsRow),
	}
}

func (r *Recorder) Start(destination, method, upstream string) func(status string) {
	started := r.now()
	key := makeStatsKey(destination, method, upstream)

	r.mu.Lock()
	row := r.rowForLocked(key, started)
	row.inflight++
	r.mu.Unlock()

	var once sync.Once
	return func(status string) {
		once.Do(func() {
			latency := r.now().Sub(started)
			r.finish(key, Observation{
				Source:      r.source,
				Destination: key.destination,
				Method:      key.method,
				Upstream:    key.upstream,
				Status:      normalizeStatus(status),
				Latency:     latency,
			})
		})
	}
}

func (r *Recorder) Observe(obs Observation) {
	key := makeStatsKey(obs.Destination, obs.Method, obs.Upstream)
	obs.Source = firstNonEmpty(obs.Source, r.source)
	obs.Destination = key.destination
	obs.Method = key.method
	obs.Upstream = key.upstream
	obs.Status = normalizeStatus(obs.Status)
	r.recordObservation(key, obs, false)
}

func (r *Recorder) Snapshot() []EndpointStats {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.snapshotLocked(false)
}

func (r *Recorder) SnapshotAndReset() []EndpointStats {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.snapshotLocked(true)
}

func (r *Recorder) finish(key statsKey, obs Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row := r.rowForLocked(key, r.now())
	if row.inflight > 0 {
		row.inflight--
	}
	r.applyObservationLocked(row, obs)
}

func (r *Recorder) recordObservation(key statsKey, obs Observation, decrementInflight bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row := r.rowForLocked(key, r.now())
	if decrementInflight && row.inflight > 0 {
		row.inflight--
	}
	r.applyObservationLocked(row, obs)
}

func (r *Recorder) applyObservationLocked(row *statsRow, obs Observation) {
	row.requestCount++
	if obs.Error || obs.Status != "OK" {
		row.errorCount++
	}
	if obs.Timeout || obs.Status == "DEADLINE_EXCEEDED" {
		row.timeoutCount++
	}
	row.ewma.Observe(obs.Latency)
	row.latencies = append(row.latencies, obs.Latency)

	if r.metrics != nil {
		r.metrics.Record(obs, row.ewma.Value(), row.inflight)
	}
}

func (r *Recorder) rowForLocked(key statsKey, now time.Time) *statsRow {
	row := r.rows[key]
	if row == nil {
		row = &statsRow{
			ewma:        NewEWMA(defaultEWMAAlpha),
			windowStart: now,
		}
		r.rows[key] = row
	}
	if row.windowStart.IsZero() {
		row.windowStart = now
	}
	return row
}

func (r *Recorder) snapshotLocked(reset bool) []EndpointStats {
	now := r.now()
	stats := make([]EndpointStats, 0, len(r.rows))
	for key, row := range r.rows {
		if row.requestCount == 0 && row.inflight == 0 {
			continue
		}
		stats = append(stats, EndpointStats{
			Source:       r.source,
			Destination:  key.destination,
			Method:       key.method,
			Upstream:     key.upstream,
			RequestCount: row.requestCount,
			ErrorCount:   row.errorCount,
			TimeoutCount: row.timeoutCount,
			Inflight:     row.inflight,
			LatencyEWMA:  row.ewma.Value(),
			LatencyP95:   percentile(row.latencies, 0.95),
			WindowStart:  row.windowStart,
			WindowEnd:    now,
		})
		if reset {
			row.requestCount = 0
			row.errorCount = 0
			row.timeoutCount = 0
			row.latencies = nil
			row.windowStart = now
		}
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Destination != stats[j].Destination {
			return stats[i].Destination < stats[j].Destination
		}
		if stats[i].Upstream != stats[j].Upstream {
			return stats[i].Upstream < stats[j].Upstream
		}
		return stats[i].Method < stats[j].Method
	})
	return stats
}

func makeStatsKey(destination, method, upstream string) statsKey {
	return statsKey{
		destination: firstNonEmpty(destination, "unknown"),
		method:      firstNonEmpty(method, "unknown"),
		upstream:    firstNonEmpty(upstream, "unknown"),
	}
}

func normalizeStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "OK"
	}
	return status
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func percentile(samples []time.Duration, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	values := append([]time.Duration(nil), samples...)
	sort.Slice(values, func(i, j int) bool {
		return values[i] < values[j]
	})
	idx := int(q*float64(len(values))+0.999999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

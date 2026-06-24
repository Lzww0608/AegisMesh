package telemetry

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	recorderShardCount      = 64
	latencyHistogramBuckets = 128
	maxLatencyBucketNanos   = uint64(time.Hour)
)

var latencyBucketUpperBounds = buildLatencyBucketUpperBounds()

type Observation struct {
	Source      string
	Destination string
	Method      string
	EndpointID  string
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
	EndpointID   string
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

type MetricLabels struct {
	Source          string
	Destination     string
	Method          string
	EndpointID      string
	EndpointAddress string
}

type MetricsSink interface {
	CreateRowMetrics(labels MetricLabels) RowMetrics
}

type RowMetrics interface {
	Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64)
}

type LegacyMetricsSink interface {
	Record(obs Observation, latencyEWMA time.Duration, inflight int64)
}

type Recorder struct {
	source  string
	metrics any
	now     func() time.Time
	shards  [recorderShardCount]recorderShard
}

type recorderShard struct {
	mu   sync.Mutex
	rows map[statsKey]*statsRow
}

type statsKey struct {
	destination string
	method      string
	endpointID  string
	upstream    string
}

type statsRow struct {
	key          statsKey
	metrics      RowMetrics
	requestCount atomic.Int64
	errorCount   atomic.Int64
	timeoutCount atomic.Int64
	inflight     atomic.Int64

	mu          sync.Mutex
	ewma        *EWMA
	activeHist  *latencyHistogram
	scratchHist *latencyHistogram
	windowStart time.Time
}

type latencyHistogram struct {
	count   uint64
	buckets [latencyHistogramBuckets]uint64
}

type legacyRowMetrics struct {
	sink   LegacyMetricsSink
	source string
	key    statsKey
}

func NewRecorder(source string, metrics any) *Recorder {
	return NewRecorderWithClock(source, metrics, time.Now)
}

func NewRecorderWithClock(source string, metrics any, now func() time.Time) *Recorder {
	if source == "" {
		source = "unknown"
	}
	if now == nil {
		now = time.Now
	}
	recorder := &Recorder{
		source:  source,
		metrics: metrics,
		now:     now,
	}
	for i := range recorder.shards {
		recorder.shards[i].rows = make(map[statsKey]*statsRow)
	}
	return recorder
}

func (r *Recorder) Start(destination, method, upstream string) func(status string) {
	started := r.now()
	key := makeStatsKey(destination, method, "", upstream)
	row := r.rowFor(key, started)
	row.inflight.Add(1)

	var once sync.Once
	return func(status string) {
		once.Do(func() {
			latency := r.now().Sub(started)
			r.finish(row, Observation{
				Source:      r.source,
				Destination: key.destination,
				Method:      key.method,
				EndpointID:  key.endpointID,
				Upstream:    key.upstream,
				Status:      normalizeStatus(status),
				Latency:     latency,
			})
		})
	}
}

func (r *Recorder) Observe(obs Observation) {
	key := makeStatsKey(obs.Destination, obs.Method, obs.EndpointID, obs.Upstream)
	obs.Source = r.source
	obs.Destination = key.destination
	obs.Method = key.method
	obs.EndpointID = key.endpointID
	obs.Upstream = key.upstream
	obs.Status = normalizeStatus(obs.Status)
	row := r.rowFor(key, r.now())
	r.recordObservation(row, obs, false)
}

func (r *Recorder) Snapshot() []EndpointStats {
	return r.snapshot(false)
}

func (r *Recorder) SnapshotAndReset() []EndpointStats {
	return r.snapshot(true)
}

func (r *Recorder) finish(row *statsRow, obs Observation) {
	r.recordObservation(row, obs, true)
}

func (r *Recorder) recordObservation(row *statsRow, obs Observation, decrementInflight bool) {
	if decrementInflight {
		decrementPositive(&row.inflight)
	}

	row.mu.Lock()
	row.requestCount.Add(1)
	if obs.Error || obs.Status != "OK" {
		row.errorCount.Add(1)
	}
	if obs.Timeout || obs.Status == "DEADLINE_EXCEEDED" {
		row.timeoutCount.Add(1)
	}
	row.ewma.Observe(obs.Latency)
	row.activeHist.Record(obs.Latency)
	latencyEWMA := row.ewma.Value()
	inflight := row.inflight.Load()
	metrics := row.metrics
	row.mu.Unlock()

	if metrics != nil {
		metrics.Record(obs.Status, obs.Latency, latencyEWMA, inflight)
	}
}

func (r *Recorder) rowFor(key statsKey, now time.Time) *statsRow {
	shard := r.shardFor(key)
	shard.mu.Lock()
	row := shard.rows[key]
	if row == nil {
		row = &statsRow{
			key:         key,
			metrics:     r.bindMetrics(key),
			ewma:        NewEWMA(defaultEWMAAlpha),
			activeHist:  &latencyHistogram{},
			scratchHist: &latencyHistogram{},
			windowStart: now,
		}
		shard.rows[key] = row
	}
	shard.mu.Unlock()
	return row
}

func (r *Recorder) bindMetrics(key statsKey) RowMetrics {
	if r.metrics == nil {
		return nil
	}
	labels := MetricLabels{
		Source:          r.source,
		Destination:     key.destination,
		Method:          key.method,
		EndpointID:      key.endpointID,
		EndpointAddress: key.upstream,
	}
	if sink, ok := r.metrics.(MetricsSink); ok {
		return sink.CreateRowMetrics(labels)
	}
	if sink, ok := r.metrics.(LegacyMetricsSink); ok {
		return legacyRowMetrics{sink: sink, source: r.source, key: key}
	}
	return nil
}

func (r *Recorder) shardFor(key statsKey) *recorderShard {
	return &r.shards[statsKeyHash(key)&(recorderShardCount-1)]
}

func (r *Recorder) snapshot(reset bool) []EndpointStats {
	now := r.now()
	stats := make([]EndpointStats, 0, r.rowCount())
	for i := range r.shards {
		shard := &r.shards[i]
		shard.mu.Lock()
		for key, row := range shard.rows {
			if stat, ok := row.snapshot(r.source, key, now, reset); ok {
				stats = append(stats, stat)
			}
		}
		shard.mu.Unlock()
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Destination != stats[j].Destination {
			return stats[i].Destination < stats[j].Destination
		}
		if stats[i].Upstream != stats[j].Upstream {
			return stats[i].Upstream < stats[j].Upstream
		}
		if stats[i].EndpointID != stats[j].EndpointID {
			return stats[i].EndpointID < stats[j].EndpointID
		}
		return stats[i].Method < stats[j].Method
	})
	return stats
}

func (r *Recorder) rowCount() int {
	count := 0
	for i := range r.shards {
		shard := &r.shards[i]
		shard.mu.Lock()
		count += len(shard.rows)
		shard.mu.Unlock()
	}
	return count
}

func (row *statsRow) snapshot(source string, key statsKey, now time.Time, reset bool) (EndpointStats, bool) {
	row.mu.Lock()
	requestCount := row.requestCount.Load()
	inflight := row.inflight.Load()
	if requestCount == 0 && inflight == 0 {
		row.mu.Unlock()
		return EndpointStats{}, false
	}

	stat := EndpointStats{
		Source:       source,
		Destination:  key.destination,
		Method:       key.method,
		EndpointID:   key.endpointID,
		Upstream:     key.upstream,
		RequestCount: requestCount,
		ErrorCount:   row.errorCount.Load(),
		TimeoutCount: row.timeoutCount.Load(),
		Inflight:     inflight,
		LatencyEWMA:  row.ewma.Value(),
		LatencyP95:   row.activeHist.Quantile(0.95),
		WindowStart:  row.windowStart,
		WindowEnd:    now,
	}
	if reset {
		row.requestCount.Store(0)
		row.errorCount.Store(0)
		row.timeoutCount.Store(0)
		row.activeHist, row.scratchHist = row.scratchHist, row.activeHist
		row.activeHist.Reset()
		row.scratchHist.Reset()
		row.windowStart = now
	}
	row.mu.Unlock()
	return stat, true
}

func (h *latencyHistogram) Record(sample time.Duration) {
	if sample < 0 {
		sample = 0
	}
	idx := latencyBucketIndex(uint64(sample))
	h.buckets[idx]++
	h.count++
}

func (h *latencyHistogram) Quantile(q float64) time.Duration {
	if h.count == 0 {
		return 0
	}
	if q <= 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	target := uint64(q*float64(h.count) + 0.999999999)
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, count := range h.buckets {
		seen += count
		if seen >= target {
			return time.Duration(latencyBucketUpperBounds[i])
		}
	}
	return time.Duration(latencyBucketUpperBounds[len(latencyBucketUpperBounds)-1])
}

func (h *latencyHistogram) Reset() {
	for i := range h.buckets {
		h.buckets[i] = 0
	}
	h.count = 0
}

func makeStatsKey(destination, method, endpointID, upstream string) statsKey {
	return statsKey{
		destination: firstNonEmpty(destination, "unknown"),
		method:      firstNonEmpty(method, "unknown"),
		endpointID:  strings.TrimSpace(endpointID),
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

func decrementPositive(value *atomic.Int64) int64 {
	for {
		current := value.Load()
		if current <= 0 {
			return 0
		}
		if value.CompareAndSwap(current, current-1) {
			return current - 1
		}
	}
}

func (m legacyRowMetrics) Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64) {
	m.sink.Record(Observation{
		Source:      m.source,
		Destination: m.key.destination,
		Method:      m.key.method,
		EndpointID:  m.key.endpointID,
		Upstream:    m.key.upstream,
		Status:      status,
		Latency:     latency,
	}, latencyEWMA, inflight)
}

func statsKeyHash(key statsKey) uint64 {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	h = hashString(h, key.destination)
	h = hashString(h, key.method)
	h = hashString(h, key.endpointID)
	h = hashString(h, key.upstream)
	return h
}

func hashString(h uint64, value string) uint64 {
	const prime64 = 1099511628211
	for i := 0; i < len(value); i++ {
		h ^= uint64(value[i])
		h *= prime64
	}
	h ^= 0xff
	h *= prime64
	return h
}

func latencyBucketIndex(nanos uint64) int {
	lo, hi := 0, len(latencyBucketUpperBounds)-1
	for lo < hi {
		mid := lo + (hi-lo)/2
		if nanos <= latencyBucketUpperBounds[mid] {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

func buildLatencyBucketUpperBounds() [latencyHistogramBuckets]uint64 {
	var bounds [latencyHistogramBuckets]uint64
	bounds[0] = 0
	bound := uint64(time.Microsecond)
	for i := 1; i < latencyHistogramBuckets-1; i++ {
		bounds[i] = bound
		next := bound + (bound+6)/7
		if next <= bound || next >= maxLatencyBucketNanos {
			bound = maxLatencyBucketNanos
		} else {
			bound = next
		}
	}
	bounds[latencyHistogramBuckets-1] = maxLatencyBucketNanos
	return bounds
}

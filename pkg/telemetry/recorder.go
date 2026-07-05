package telemetry

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/align"
)

const (
	recorderShardCount      = 64
	latencyHistogramBuckets = 128
	maxLatencyBucketNanos   = uint64(time.Hour)
)

var latencyBucketUpperBounds = buildLatencyBucketUpperBounds()

// Observation carries observation state for recorder aggregation.
type Observation struct {
	Source            string
	Destination       string
	Method            string
	EndpointID        string
	RegistrationEpoch string
	Upstream          string
	Status            string
	Latency           time.Duration
	Error             bool
	Timeout           bool
}

// EndpointStats carries endpoint stats state for recorder aggregation.
type EndpointStats struct {
	Source            string
	Destination       string
	Method            string
	EndpointID        string
	RegistrationEpoch string
	Upstream          string
	RequestCount      int64
	ErrorCount        int64
	TimeoutCount      int64
	Inflight          int64
	LatencyEWMA       time.Duration
	LatencyP95        time.Duration
	WindowStart       time.Time
	WindowEnd         time.Time
}

// MetricLabels carries metric labels state for recorder aggregation.
type MetricLabels struct {
	Source            string
	Destination       string
	Method            string
	EndpointID        string
	RegistrationEpoch string
	EndpointAddress   string
}

// MetricsSink defines the metrics sink contract used by recorder aggregation.
type MetricsSink interface {
	CreateRowMetrics(labels MetricLabels) RowMetrics
}

// RowMetrics owns metric collectors for row metrics observations.
type RowMetrics interface {
	Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64)
}

// LegacyMetricsSink defines the legacy metrics sink contract used by recorder aggregation.
type LegacyMetricsSink interface {
	Record(obs Observation, latencyEWMA time.Duration, inflight int64)
}

// Recorder carries recorder state for recorder aggregation.
type Recorder struct {
	source  string
	metrics any
	now     func() time.Time
	shards  [recorderShardCount]recorderShard
}

// recorderShard carries recorder shard state for recorder aggregation.
type recorderShard struct {
	mu   sync.Mutex
	rows map[statsKey]*statsRow
}

// statsKey carries stats key state for recorder aggregation.
type statsKey struct {
	destination       string
	method            string
	endpointID        string
	registrationEpoch string
	upstream          string
}

// statsRow carries stats row state for recorder aggregation.
type statsRow struct {
	key         statsKey
	metrics     RowMetrics
	counters    statsCounters
	mu          sync.Mutex
	ewma        *EWMA
	activeHist  *shardedLatencyHistogram
	scratchHist *shardedLatencyHistogram
	windowStart time.Time
}

// statsCounters carries stats counters state for recorder aggregation.
type statsCounters struct {
	requestCount atomic.Int64
	errorCount   atomic.Int64
	timeoutCount atomic.Int64
	inflight     atomic.Int64
	_            align.Pad48
}

// legacyRowMetrics owns metric collectors for legacy row metrics observations.
type legacyRowMetrics struct {
	sink   LegacyMetricsSink
	source string
	key    statsKey
}

// NewRecorder initializes recorder with package defaults for this package's call path.
func NewRecorder(source string, metrics any) *Recorder {
	return NewRecorderWithClock(source, metrics, time.Now)
}

// NewRecorderWithClock initializes recorder with clock with package defaults for this package's call path.
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

// Start begins collection and binds the collector lifetime to its owned resources.
func (r *Recorder) Start(destination, method, upstream string) func(status string) {
	started := r.now()
	key := makeStatsKey(destination, method, "", "", upstream)
	row := r.rowFor(key, started)
	row.counters.inflight.Add(1)

	var once sync.Once
	return func(status string) {
		once.Do(func() {
			latency := r.now().Sub(started)
			r.finish(row, Observation{
				Source:            r.source,
				Destination:       key.destination,
				Method:            key.method,
				EndpointID:        key.endpointID,
				RegistrationEpoch: key.registrationEpoch,
				Upstream:          key.upstream,
				Status:            normalizeStatus(status),
				Latency:           latency,
			})
		})
	}
}

// Observe observes observe and folds it into the current aggregate.
func (r *Recorder) Observe(obs Observation) {
	key := makeStatsKey(obs.Destination, obs.Method, obs.EndpointID, obs.RegistrationEpoch, obs.Upstream)
	obs.Source = r.source
	obs.Destination = key.destination
	obs.Method = key.method
	obs.EndpointID = key.endpointID
	obs.RegistrationEpoch = key.registrationEpoch
	obs.Upstream = key.upstream
	obs.Status = normalizeStatus(obs.Status)
	row := r.rowFor(key, r.now())
	r.recordObservation(row, obs, false)
}

// Snapshot returns an immutable snapshot of the current snapshot state.
func (r *Recorder) Snapshot() []EndpointStats {
	stats := r.snapshot(false)
	if len(stats) == 0 {
		ReleaseEndpointStatsSlice(stats)
		return nil
	}
	clone := cloneEndpointStatsSlice(stats)
	ReleaseEndpointStatsSlice(stats)
	return clone
}

// SnapshotAndReset returns a pooled stats slice. Call ReleaseEndpointStatsSlice when done.
func (r *Recorder) SnapshotAndReset() []EndpointStats {
	return r.snapshot(true)
}

// finish records a completed observation and decrements inflight accounting exactly once.
func (r *Recorder) finish(row *statsRow, obs Observation) {
	r.recordObservation(row, obs, true)
}

// recordObservation records record observation in the current accounting window.
func (r *Recorder) recordObservation(row *statsRow, obs Observation, decrementInflight bool) {
	if decrementInflight {
		decrementPositive(&row.counters.inflight)
	}

	row.counters.requestCount.Add(1)
	if obs.Error || obs.Status != "OK" {
		row.counters.errorCount.Add(1)
	}
	if obs.Timeout || obs.Status == "DEADLINE_EXCEEDED" {
		row.counters.timeoutCount.Add(1)
	}

	row.mu.Lock()
	row.ewma.Observe(obs.Latency)
	row.activeHist.Record(obs.Latency)
	latencyEWMA := row.ewma.Value()
	inflight := row.counters.inflight.Load()
	metrics := row.metrics
	row.mu.Unlock()

	if metrics != nil {
		metrics.Record(obs.Status, obs.Latency, latencyEWMA, inflight)
	}
}

// rowFor returns row for data for Recorder callers without handing out mutable receiver state.
func (r *Recorder) rowFor(key statsKey, now time.Time) *statsRow {
	shard := r.shardFor(key)
	shard.mu.Lock()
	row := shard.rows[key]
	if row == nil {
		row = &statsRow{
			key:         key,
			metrics:     r.bindMetrics(key),
			ewma:        NewEWMA(defaultEWMAAlpha),
			activeHist:  &shardedLatencyHistogram{},
			scratchHist: &shardedLatencyHistogram{},
			windowStart: now,
		}
		shard.rows[key] = row
	}
	shard.mu.Unlock()
	return row
}

// bindMetrics returns bind metrics data for Recorder callers without handing out mutable receiver state.
func (r *Recorder) bindMetrics(key statsKey) RowMetrics {
	if r.metrics == nil {
		return nil
	}
	labels := MetricLabels{
		Source:            r.source,
		Destination:       key.destination,
		Method:            key.method,
		EndpointID:        key.endpointID,
		RegistrationEpoch: key.registrationEpoch,
		EndpointAddress:   key.upstream,
	}
	if sink, ok := r.metrics.(MetricsSink); ok {
		return sink.CreateRowMetrics(labels)
	}
	if sink, ok := r.metrics.(LegacyMetricsSink); ok {
		return legacyRowMetrics{sink: sink, source: r.source, key: key}
	}
	return nil
}

// shardFor returns shard for data for Recorder callers without handing out mutable receiver state.
func (r *Recorder) shardFor(key statsKey) *recorderShard {
	return &r.shards[statsKeyHash(key)&(recorderShardCount-1)]
}

// snapshot returns an immutable snapshot of the current snapshot state.
func (r *Recorder) snapshot(reset bool) []EndpointStats {
	now := r.now()
	stats := acquireEndpointStatsSlice(r.rowCount())
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

// rowCount returns row count data for Recorder callers without handing out mutable receiver state.
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

// snapshot returns an immutable snapshot of the current snapshot state.
func (row *statsRow) snapshot(source string, key statsKey, now time.Time, reset bool) (EndpointStats, bool) {
	row.mu.Lock()
	requestCount := row.counters.requestCount.Load()
	inflight := row.counters.inflight.Load()
	if requestCount == 0 && inflight == 0 {
		row.mu.Unlock()
		return EndpointStats{}, false
	}

	stat := EndpointStats{
		Source:            source,
		Destination:       key.destination,
		Method:            key.method,
		EndpointID:        key.endpointID,
		RegistrationEpoch: key.registrationEpoch,
		Upstream:          key.upstream,
		RequestCount:      requestCount,
		ErrorCount:        row.counters.errorCount.Load(),
		TimeoutCount:      row.counters.timeoutCount.Load(),
		Inflight:          inflight,
		LatencyEWMA:       row.ewma.Value(),
		LatencyP95:        row.activeHist.Quantile(0.95),
		WindowStart:       row.windowStart,
		WindowEnd:         now,
	}
	if reset {
		row.counters.requestCount.Store(0)
		row.counters.errorCount.Store(0)
		row.counters.timeoutCount.Store(0)
		row.activeHist, row.scratchHist = row.scratchHist, row.activeHist
		row.activeHist.Reset()
		row.scratchHist.Reset()
		row.windowStart = now
	}
	row.mu.Unlock()
	return stat, true
}

// makeStatsKey folds endpoint identity and registration epoch into recorder shard identity.
func makeStatsKey(destination, method, endpointID, registrationEpoch, upstream string) statsKey {
	return statsKey{
		destination:       firstNonEmpty(destination, "unknown"),
		method:            firstNonEmpty(method, "unknown"),
		endpointID:        strings.TrimSpace(endpointID),
		registrationEpoch: strings.TrimSpace(registrationEpoch),
		upstream:          firstNonEmpty(upstream, "unknown"),
	}
}

// normalizeStatus normalizes normalize status so downstream logic sees one canonical form.
func normalizeStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "OK"
	}
	return status
}

// firstNonEmpty preserves explicit values while providing legacy fallbacks.
func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// decrementPositive prevents inflight counters from underflowing under duplicate completions.
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

// Record records record in the current accounting window.
func (m legacyRowMetrics) Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64) {
	m.sink.Record(Observation{
		Source:            m.source,
		Destination:       m.key.destination,
		Method:            m.key.method,
		EndpointID:        m.key.endpointID,
		RegistrationEpoch: m.key.registrationEpoch,
		Upstream:          m.key.upstream,
		Status:            status,
		Latency:           latency,
	}, latencyEWMA, inflight)
}

// statsKeyHash chooses the recorder shard for a stable endpoint/method identity.
func statsKeyHash(key statsKey) uint64 {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	h = hashString(h, key.destination)
	h = hashString(h, key.method)
	h = hashString(h, key.endpointID)
	h = hashString(h, key.registrationEpoch)
	h = hashString(h, key.upstream)
	return h
}

// hashString mixes string fields into the recorder shard hash without allocating.
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

// latencyBucketIndex maps nanosecond latency into the fixed recorder histogram buckets.
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

// buildLatencyBucketUpperBounds builds build latency bucket upper bounds dependencies from validated configuration.
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

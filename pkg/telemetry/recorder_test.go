package telemetry

import (
	"sync"
	"testing"
	"time"
)

func TestRecorderAggregatesEndpointWindowStats(t *testing.T) {
	recorder := NewRecorder("frontend", nil)

	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	})
	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "DEADLINE_EXCEEDED",
		Latency:     300 * time.Millisecond,
		Error:       true,
		Timeout:     true,
	})

	stats := recorder.SnapshotAndReset()
	defer ReleaseEndpointStatsSlice(stats)
	if len(stats) != 1 {
		t.Fatalf("expected one endpoint stats row, got %d", len(stats))
	}
	got := stats[0]
	if got.Source != "frontend" || got.Destination != "user-service" || got.Upstream != "127.0.0.1:7001" {
		t.Fatalf("unexpected stats identity: %+v", got)
	}
	if got.RequestCount != 2 || got.ErrorCount != 1 || got.TimeoutCount != 1 {
		t.Fatalf("unexpected request counters: %+v", got)
	}
	if got.LatencyEWMA != 140*time.Millisecond {
		t.Fatalf("expected EWMA 140ms, got %s", got.LatencyEWMA)
	}
	assertDurationBetween(t, got.LatencyP95, 300*time.Millisecond, 330*time.Millisecond)

	remaining := recorder.SnapshotAndReset()
	defer ReleaseEndpointStatsSlice(remaining)
	if len(remaining) != 0 {
		t.Fatalf("expected snapshot to reset window, got %+v", remaining)
	}
}

func TestRecorderTracksInflightAroundCalls(t *testing.T) {
	recorder := NewRecorder("frontend", nil)
	finish := recorder.Start("user-service", "/demo.shop.v1.UserService/GetUser", "127.0.0.1:7001")

	active := recorder.Snapshot()
	if len(active) != 1 {
		t.Fatalf("expected active stats row, got %d", len(active))
	}
	if active[0].Inflight != 1 {
		t.Fatalf("expected inflight 1, got %d", active[0].Inflight)
	}

	finish("OK")
	done := recorder.Snapshot()
	if len(done) != 1 {
		t.Fatalf("expected finished stats row, got %d", len(done))
	}
	if done[0].Inflight != 0 || done[0].RequestCount != 1 {
		t.Fatalf("expected finished call to decrement inflight and record request, got %+v", done[0])
	}
}

func TestRecorderObserveDoesNotAllocateOnWarmRow(t *testing.T) {
	recorder := NewRecorderWithClock("frontend", nil, fixedTestTime)
	obs := Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	}
	recorder.Observe(obs)

	allocs := testing.AllocsPerRun(1000, func() {
		recorder.Observe(obs)
	})
	if allocs != 0 {
		t.Fatalf("expected warm Observe to allocate zero objects, got %.2f", allocs)
	}
}

func TestRecorderCreatesRowMetricsOncePerStatsRow(t *testing.T) {
	sink := &countingMetricsSink{}
	recorder := NewRecorderWithClock("frontend", sink, fixedTestTime)
	obs := Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	}

	recorder.Observe(obs)
	recorder.Observe(obs)

	if sink.created != 1 {
		t.Fatalf("expected one RowMetrics for one stats row, got %d", sink.created)
	}
	if sink.row.records != 2 {
		t.Fatalf("expected cached RowMetrics to record two observations, got %d", sink.row.records)
	}
	if sink.labels.Source != "frontend" || sink.labels.EndpointID != "user-a" || sink.labels.EndpointAddress != "127.0.0.1:7001" {
		t.Fatalf("unexpected row metric labels: %+v", sink.labels)
	}
}

func TestRecorderKeepsLegacyRecordMetricsCompatibility(t *testing.T) {
	sink := &legacyCountingMetricsSink{}
	recorder := NewRecorderWithClock("frontend", sink, fixedTestTime)

	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	})

	if sink.records != 1 {
		t.Fatalf("expected one legacy Record call, got %d", sink.records)
	}
	if sink.last.EndpointID != "user-a" || sink.last.Upstream != "127.0.0.1:7001" {
		t.Fatalf("unexpected legacy observation: %+v", sink.last)
	}
}

func TestRecorderAggregatesConcurrentObservations(t *testing.T) {
	recorder := NewRecorderWithClock("frontend", nil, fixedTestTime)
	obs := Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     25 * time.Millisecond,
	}

	const goroutines = 100
	const perGoroutine = 100
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perGoroutine; j++ {
				recorder.Observe(obs)
			}
		}()
	}
	close(start)
	wg.Wait()

	stats := recorder.SnapshotAndReset()
	defer ReleaseEndpointStatsSlice(stats)
	if len(stats) != 1 {
		t.Fatalf("expected one stats row, got %d", len(stats))
	}
	if stats[0].RequestCount != goroutines*perGoroutine {
		t.Fatalf("expected %d requests, got %+v", goroutines*perGoroutine, stats[0])
	}
	assertDurationBetween(t, stats[0].LatencyP95, 25*time.Millisecond, 30*time.Millisecond)
}

func TestLatencyHistogramApproximatesP95(t *testing.T) {
	var hist latencyHistogram
	for i := 1; i <= 100; i++ {
		hist.Record(time.Duration(i) * time.Millisecond)
	}

	p95 := hist.Quantile(0.95)
	assertDurationBetween(t, p95, 95*time.Millisecond, 110*time.Millisecond)
}

func TestRecorderApproximateP95PreservesSlowEndpointOrdering(t *testing.T) {
	recorder := NewRecorderWithClock("frontend", nil, fixedTestTime)
	for i := 0; i < 100; i++ {
		recorder.Observe(Observation{Destination: "user-service", Method: "/demo/Get", Upstream: "fast", Status: "OK", Latency: 100 * time.Millisecond})
		recorder.Observe(Observation{Destination: "user-service", Method: "/demo/Get", Upstream: "slow", Status: "OK", Latency: 600 * time.Millisecond})
	}

	stats := recorder.SnapshotAndReset()
	defer ReleaseEndpointStatsSlice(stats)
	if len(stats) != 2 {
		t.Fatalf("expected two stats rows, got %+v", stats)
	}
	byUpstream := map[string]EndpointStats{}
	for _, stat := range stats {
		byUpstream[stat.Upstream] = stat
	}
	fast := byUpstream["fast"].LatencyP95
	slow := byUpstream["slow"].LatencyP95
	if slow <= fast*4 {
		t.Fatalf("expected approximate p95 to preserve slow endpoint ordering, fast=%s slow=%s", fast, slow)
	}
}

func assertDurationBetween(t *testing.T, got, min, max time.Duration) {
	t.Helper()
	if got < min || got > max {
		t.Fatalf("expected duration in [%s, %s], got %s", min, max, got)
	}
}

func fixedTestTime() time.Time {
	return time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
}

type countingMetricsSink struct {
	created int
	labels  MetricLabels
	row     countingRowMetrics
}

func (s *countingMetricsSink) CreateRowMetrics(labels MetricLabels) RowMetrics {
	s.created++
	s.labels = labels
	return &s.row
}

type countingRowMetrics struct {
	records int
}

func (m *countingRowMetrics) Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64) {
	m.records++
}

type legacyCountingMetricsSink struct {
	records int
	last    Observation
}

func (s *legacyCountingMetricsSink) Record(obs Observation, latencyEWMA time.Duration, inflight int64) {
	s.records++
	s.last = obs
}

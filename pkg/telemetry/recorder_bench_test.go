package telemetry

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var benchmarkRecorderStats []EndpointStats

// BenchmarkShardedLatencyHistogramRecordParallel reports latency and allocation cost for sharded latency histogram record parallel.
func BenchmarkShardedLatencyHistogramRecordParallel(b *testing.B) {
	var hist shardedLatencyHistogram
	sample := 750 * time.Microsecond

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			hist.Record(sample)
		}
	})
}

// BenchmarkLatencyHistogramRecordParallel reports latency and allocation cost for latency histogram record parallel.
func BenchmarkLatencyHistogramRecordParallel(b *testing.B) {
	var hist latencyHistogram
	sample := 750 * time.Microsecond

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			hist.Record(sample)
		}
	})
}

// BenchmarkRecorderObserve reports latency and allocation cost for recorder observe.
func BenchmarkRecorderObserve(b *testing.B) {
	recorder := NewRecorderWithClock("bench-client", nil, fixedBenchmarkTime)
	obs := Observation{
		Destination: "bench-service",
		Method:      "/bench.Service/Get",
		Upstream:    "10.0.0.1:7001",
		Status:      "OK",
		Latency:     750 * time.Microsecond,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.Observe(obs)
	}
}

// BenchmarkRecorderObserveParallel reports latency and allocation cost for recorder observe parallel.
func BenchmarkRecorderObserveParallel(b *testing.B) {
	recorder := NewRecorderWithClock("bench-client", nil, fixedBenchmarkTime)
	obs := Observation{
		Destination: "bench-service",
		Method:      "/bench.Service/Get",
		Upstream:    "10.0.0.1:7001",
		Status:      "OK",
		Latency:     750 * time.Microsecond,
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			recorder.Observe(obs)
		}
	})
}

// BenchmarkRecorderObserveParallelSharded reports latency and allocation cost for recorder observe parallel sharded.
func BenchmarkRecorderObserveParallelSharded(b *testing.B) {
	recorder := NewRecorderWithClock("bench-client", nil, fixedBenchmarkTime)
	observations := make([]Observation, 64)
	for i := range observations {
		observations[i] = Observation{
			Destination: "bench-service",
			Method:      "/bench.Service/Get",
			Upstream:    fmt.Sprintf("10.0.0.%d:7001", i+1),
			Status:      "OK",
			Latency:     750 * time.Microsecond,
		}
		recorder.Observe(observations[i])
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			recorder.Observe(observations[i&63])
			i++
		}
	})
}

// BenchmarkPrometheusRecordCompatibility reports latency and allocation cost for prometheus record compatibility.
func BenchmarkPrometheusRecordCompatibility(b *testing.B) {
	metrics, err := NewPrometheusMetrics(prometheus.NewRegistry())
	if err != nil {
		b.Fatalf("create prometheus metrics: %v", err)
	}
	obs := Observation{
		Source:      "bench-client",
		Destination: "bench-service",
		Method:      "/bench.Service/Get",
		EndpointID:  "bench-a",
		Upstream:    "10.0.0.1:7001",
		Status:      "OK",
		Latency:     750 * time.Microsecond,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metrics.Record(obs, obs.Latency, 0)
	}
}

// BenchmarkRecorderObserveWithPrometheusCachedRow reports latency and allocation cost for recorder observe with prometheus cached row.
func BenchmarkRecorderObserveWithPrometheusCachedRow(b *testing.B) {
	metrics, err := NewPrometheusMetrics(prometheus.NewRegistry())
	if err != nil {
		b.Fatalf("create prometheus metrics: %v", err)
	}
	recorder := NewRecorderWithClock("bench-client", metrics, fixedBenchmarkTime)
	obs := Observation{
		Destination: "bench-service",
		Method:      "/bench.Service/Get",
		EndpointID:  "bench-a",
		Upstream:    "10.0.0.1:7001",
		Status:      "OK",
		Latency:     750 * time.Microsecond,
	}
	recorder.Observe(obs)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.Observe(obs)
	}
}

// BenchmarkRecorderSnapshotAndResetP95 reports latency and allocation cost for recorder snapshot and reset p95.
func BenchmarkRecorderSnapshotAndResetP95(b *testing.B) {
	upstreamCounts := []int{1, 8, 64}
	observationCounts := []int{1_000, 10_000, 100_000}

	for _, upstreams := range upstreamCounts {
		for _, observations := range observationCounts {
			name := fmt.Sprintf("upstreams=%d/observations=%d", upstreams, observations)
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					recorder := newBenchmarkRecorder(upstreams, observations)
					b.StartTimer()
					benchmarkRecorderStats = recorder.SnapshotAndReset()
					b.StopTimer()
					if len(benchmarkRecorderStats) != upstreams {
						b.Fatalf("expected %d upstream rows, got %d", upstreams, len(benchmarkRecorderStats))
					}
					b.StartTimer()
				}
			})
		}
	}
}

// fixedBenchmarkTime provides the shared fixed benchmark time helper for recorder aggregation.
func fixedBenchmarkTime() time.Time {
	return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
}

// newBenchmarkRecorder initializes benchmark recorder with package defaults for this package's call path.
func newBenchmarkRecorder(upstreams, observations int) *Recorder {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	recorder := NewRecorderWithClock("bench-client", nil, func() time.Time { return now })
	for i := 0; i < observations; i++ {
		recorder.Observe(Observation{
			Destination: "bench-service",
			Method:      "/bench.Service/Get",
			Upstream:    fmt.Sprintf("10.0.0.%d:7001", i%upstreams+1),
			Status:      "OK",
			Latency:     time.Duration(i%1000+1) * time.Microsecond,
		})
	}
	return recorder
}

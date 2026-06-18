package telemetry

import (
	"fmt"
	"testing"
	"time"
)

var benchmarkRecorderStats []EndpointStats

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

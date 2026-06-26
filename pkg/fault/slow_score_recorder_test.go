package fault

import (
	"testing"
	"time"

	"github.com/aegismesh/aegismesh/pkg/telemetry"
)

func TestScoreCalculatorFlagsSlowEndpointFromRecorderApproxP95(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	recorder := telemetry.NewRecorderWithClock("frontend", nil, func() time.Time { return now })
	for i := 0; i < 100; i++ {
		recorder.Observe(telemetry.Observation{Destination: "user-service", Method: "/demo/Get", Upstream: "user-a", Status: "OK", Latency: 100 * time.Millisecond})
		recorder.Observe(telemetry.Observation{Destination: "user-service", Method: "/demo/Get", Upstream: "user-b", Status: "OK", Latency: 110 * time.Millisecond})
		recorder.Observe(telemetry.Observation{Destination: "user-service", Method: "/demo/Get", Upstream: "user-c", Status: "OK", Latency: 600 * time.Millisecond})
	}

	stats := recorder.SnapshotAndReset()
	defer telemetry.ReleaseEndpointStatsSlice(stats)
	samples := make([]EndpointSample, 0, len(stats))
	for _, stat := range stats {
		samples = append(samples, EndpointSample{
			Service:      stat.Destination,
			InstanceID:   stat.Upstream,
			Address:      stat.Upstream,
			Method:       stat.Method,
			RequestCount: stat.RequestCount,
			ErrorCount:   stat.ErrorCount,
			TimeoutCount: stat.TimeoutCount,
			Inflight:     stat.Inflight,
			Capacity:     100,
			LatencyEWMA:  stat.LatencyEWMA,
			LatencyP95:   stat.LatencyP95,
		})
	}

	scores := NewScoreCalculator(DefaultScoreWeights()).Calculate(samples)
	slow := scores[ScoreKey("user-service", "user-c")]
	fastA := scores[ScoreKey("user-service", "user-a")]
	fastB := scores[ScoreKey("user-service", "user-b")]
	if slow.Score <= 1.0 {
		t.Fatalf("expected slow endpoint score above trigger range, got slow=%+v scores=%+v", slow, scores)
	}
	if slow.Score <= fastA.Score || slow.Score <= fastB.Score {
		t.Fatalf("expected slow endpoint to outrank fast endpoints, slow=%+v fastA=%+v fastB=%+v", slow, fastA, fastB)
	}
}

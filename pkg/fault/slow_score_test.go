package fault

import (
	"testing"
	"time"
)

func TestScoreCalculatorFlagsRelativeSlowEndpoint(t *testing.T) {
	calculator := NewScoreCalculator(DefaultScoreWeights())

	scores := calculator.Calculate([]EndpointSample{
		{
			Service:       "user-service",
			InstanceID:    "user-a",
			RequestCount:  100,
			LatencyP95:    100 * time.Millisecond,
			ErrorCount:    0,
			Inflight:      10,
			Capacity:      100,
			TCPRetransmit: 0,
		},
		{
			Service:       "user-service",
			InstanceID:    "user-b",
			RequestCount:  100,
			LatencyP95:    110 * time.Millisecond,
			ErrorCount:    1,
			Inflight:      12,
			Capacity:      100,
			TCPRetransmit: 0,
		},
		{
			Service:       "user-service",
			InstanceID:    "user-c",
			RequestCount:  100,
			LatencyP95:    600 * time.Millisecond,
			ErrorCount:    30,
			Inflight:      95,
			Capacity:      100,
			TCPRetransmit: 10,
		},
	})

	slow := scores["user-service/user-c"]
	healthy := scores["user-service/user-a"]
	if slow.Score <= 2.5 {
		t.Fatalf("expected slow endpoint score above eject threshold, got %+v", slow)
	}
	if healthy.Score >= 1.5 {
		t.Fatalf("expected healthy endpoint below degraded threshold, got %+v", healthy)
	}
	if slow.LatencyScore <= healthy.LatencyScore {
		t.Fatalf("expected slow endpoint to have higher latency score: slow=%+v healthy=%+v", slow, healthy)
	}
}

func TestScoreCalculatorKeepsAllHealthyWhenServiceHasNoRelativeOutlier(t *testing.T) {
	calculator := NewScoreCalculator(DefaultScoreWeights())

	scores := calculator.Calculate([]EndpointSample{
		{Service: "order-service", InstanceID: "order-a", RequestCount: 100, LatencyP95: 100 * time.Millisecond, Capacity: 100},
		{Service: "order-service", InstanceID: "order-b", RequestCount: 100, LatencyP95: 105 * time.Millisecond, Capacity: 100},
		{Service: "order-service", InstanceID: "order-c", RequestCount: 100, LatencyP95: 110 * time.Millisecond, Capacity: 100},
	})

	for id, score := range scores {
		if score.Score >= 1.5 {
			t.Fatalf("expected %s below degraded threshold, got %+v", id, score)
		}
	}
}

func TestScoreCalculatorUsesNetworkSignalWithoutRequestCount(t *testing.T) {
	calculator := NewScoreCalculator(DefaultScoreWeights())

	scores := calculator.Calculate([]EndpointSample{
		{Service: "user-service", InstanceID: "user-a", TCPRetransmit: 0},
		{Service: "user-service", InstanceID: "user-b", TCPRetransmit: 10, ConnectError: 2},
	})

	if scores["user-service/user-b"].RetransmitScore <= scores["user-service/user-a"].RetransmitScore {
		t.Fatalf("expected network outlier to have higher retransmit score: %+v", scores)
	}
	if scores["user-service/user-b"].Score <= 0 {
		t.Fatalf("expected network-only signal to affect slow score: %+v", scores["user-service/user-b"])
	}
}

func TestScoreCalculatorUsesAbsoluteSLOWhenAllEndpointsAreSlow(t *testing.T) {
	calculator := NewScoreCalculatorWithConfig(ScoreCalculatorConfig{
		Weights:    ScoreWeights{LatencyWeight: 1},
		LatencySLO: 100 * time.Millisecond,
	})

	scores := calculator.Calculate([]EndpointSample{
		{Service: "payment-service", InstanceID: "payment-a", RequestCount: 100, LatencyP95: 450 * time.Millisecond, Capacity: 100},
		{Service: "payment-service", InstanceID: "payment-b", RequestCount: 100, LatencyP95: 430 * time.Millisecond, Capacity: 100},
	})

	for id, score := range scores {
		if score.LatencyScore < 4.0 {
			t.Fatalf("expected %s absolute SLO latency score around 4x, got %+v", id, score)
		}
		if score.Score < 4.0 {
			t.Fatalf("expected %s total score to reflect absolute SLO breach, got %+v", id, score)
		}
	}
}

func TestScoreCalculatorLeavesAbsoluteSLODisabledByDefault(t *testing.T) {
	calculator := NewScoreCalculator(ScoreWeights{LatencyWeight: 1})

	scores := calculator.Calculate([]EndpointSample{
		{Service: "payment-service", InstanceID: "payment-a", RequestCount: 100, LatencyP95: 450 * time.Millisecond, Capacity: 100},
		{Service: "payment-service", InstanceID: "payment-b", RequestCount: 100, LatencyP95: 430 * time.Millisecond, Capacity: 100},
	})

	for id, score := range scores {
		if score.LatencyScore >= 4.0 {
			t.Fatalf("expected %s to rely on relative score without configured SLO, got %+v", id, score)
		}
	}
}

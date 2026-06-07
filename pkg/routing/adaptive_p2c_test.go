package routing

import (
	"context"
	"testing"
	"time"
)

func TestAdaptiveP2CPickerChoosesLowerCostEndpoint(t *testing.T) {
	picker := NewAdaptiveP2CPicker([]Endpoint{
		{ID: "fast", Address: "127.0.0.1:7001", Status: EndpointHealthy, Inflight: 2, LatencyEWMA: 50 * time.Millisecond, Weight: 1, SlowScore: 0.1},
		{ID: "slow", Address: "127.0.0.1:7002", Status: EndpointHealthy, Inflight: 1, LatencyEWMA: 500 * time.Millisecond, Weight: 1, SlowScore: 3.0},
	}, AdaptiveP2CConfig{
		Random: &sequenceRandom{values: []int{0, 1}},
	})

	got, err := picker.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick endpoint: %v", err)
	}
	if got.ID != "fast" {
		t.Fatalf("expected lower cost endpoint fast, got %+v", got)
	}
}

func TestAdaptiveP2CPickerFiltersEjectedAndDeadEndpoints(t *testing.T) {
	picker := NewAdaptiveP2CPicker([]Endpoint{
		{ID: "dead", Address: "127.0.0.1:7001", Status: EndpointDead, Inflight: 0},
		{ID: "ejected", Address: "127.0.0.1:7002", Status: EndpointEjected, Inflight: 0},
		{ID: "healthy", Address: "127.0.0.1:7003", Status: EndpointHealthy, Inflight: 10},
	}, AdaptiveP2CConfig{
		Random: &sequenceRandom{values: []int{0, 1}},
	})

	got, err := picker.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick endpoint: %v", err)
	}
	if got.ID != "healthy" {
		t.Fatalf("expected only routable endpoint healthy, got %+v", got)
	}
}

func TestAdaptiveP2CPickerFallsBackToLeastBadDegradedEndpoint(t *testing.T) {
	picker := NewAdaptiveP2CPicker([]Endpoint{
		{ID: "worse", Address: "127.0.0.1:7001", Status: EndpointDegraded, Inflight: 10, LatencyEWMA: 300 * time.Millisecond, SlowScore: 2.0},
		{ID: "least-bad", Address: "127.0.0.1:7002", Status: EndpointDegraded, Inflight: 1, LatencyEWMA: 100 * time.Millisecond, SlowScore: 1.2},
	}, AdaptiveP2CConfig{
		LeastBadFallback: true,
		Random:           &sequenceRandom{values: []int{0, 1}},
	})

	got, err := picker.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick endpoint: %v", err)
	}
	if got.ID != "least-bad" {
		t.Fatalf("expected least-bad degraded endpoint, got %+v", got)
	}
}

type sequenceRandom struct {
	values []int
	next   int
}

func (r *sequenceRandom) Intn(n int) int {
	if n <= 0 || len(r.values) == 0 {
		return 0
	}
	value := r.values[r.next%len(r.values)]
	r.next++
	return value % n
}

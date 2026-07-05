package routing

import (
	"context"
	"testing"
	"time"
)

// TestAdaptiveP2CPickerChoosesLowerCostEndpoint locks the adaptive p2 c picker chooses lower cost endpoint contract so future changes do not regress it.
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

// TestAdaptiveP2CPickerFiltersEjectedAndDeadEndpoints locks the adaptive p2 c picker filters ejected and dead endpoints contract so future changes do not regress it.
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

// TestAdaptiveP2CPickerFallsBackToLeastBadDegradedEndpoint locks the adaptive p2 c picker falls back to least bad degraded endpoint contract so future changes do not regress it.
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

// TestAdaptiveP2CPickerKeepsProbingEndpointOutOfNormalTraffic locks the adaptive p2 c picker keeps probing endpoint out of normal traffic contract so future changes do not regress it.
func TestAdaptiveP2CPickerKeepsProbingEndpointOutOfNormalTraffic(t *testing.T) {
	picker := NewAdaptiveP2CPicker([]Endpoint{
		{ID: "healthy", Address: "127.0.0.1:7001", Status: EndpointHealthy, LatencyEWMA: 20 * time.Millisecond},
		{ID: "probe", Address: "127.0.0.1:7002", Status: EndpointProbing, LatencyEWMA: 1 * time.Millisecond},
	}, AdaptiveP2CConfig{
		ProbeRatio: 0.01,
		Random:     &sequenceRandom{values: []int{99}},
	})

	got, err := picker.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick endpoint: %v", err)
	}
	if got.ID != "healthy" {
		t.Fatalf("expected normal traffic to avoid probing endpoint, got %+v", got)
	}
}

// TestAdaptiveP2CPickerAllowsConfiguredProbeSample locks the adaptive p2 c picker allows configured probe sample contract so future changes do not regress it.
func TestAdaptiveP2CPickerAllowsConfiguredProbeSample(t *testing.T) {
	picker := NewAdaptiveP2CPicker([]Endpoint{
		{ID: "healthy", Address: "127.0.0.1:7001", Status: EndpointHealthy, LatencyEWMA: 20 * time.Millisecond},
		{ID: "probe", Address: "127.0.0.1:7002", Status: EndpointProbing, LatencyEWMA: 1 * time.Millisecond},
	}, AdaptiveP2CConfig{
		ProbeRatio: 0.50,
		Random:     &sequenceRandom{values: []int{0}},
	})

	got, err := picker.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick endpoint: %v", err)
	}
	if got.ID != "probe" {
		t.Fatalf("expected configured probe sample to select probing endpoint, got %+v", got)
	}
}

// sequenceRandom carries sequence random state for this package call path.
type sequenceRandom struct {
	values []int
	next   int
}

// Intn returns intn data for sequenceRandom callers without handing out mutable receiver state.
func (r *sequenceRandom) Intn(n int) int {
	if n <= 0 || len(r.values) == 0 {
		return 0
	}
	value := r.values[r.next%len(r.values)]
	r.next++
	return value % n
}

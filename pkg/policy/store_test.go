package policy

import (
	"os"
	"testing"
)

func TestFileStoreLoadsServicePolicySnapshot(t *testing.T) {
	path := t.TempDir() + "/policy.yaml"
	if err := os.WriteFile(path, []byte(`
services:
  user-service:
    routing_policy: adaptive_p2c
    retry:
      enabled: true
      max_attempts: 2
      budget_ratio: 0.15
      min_budget: 10
      window_seconds: 10
      per_try_timeout_millis: 750
    outlier_detection:
      degraded_threshold: 1.5
      eject_threshold: 2.5
      consecutive_windows: 3
      ejection_duration_seconds: 30
      recovery_threshold: 1.0
      probe_success_threshold: 0.95
    circuit_breaker:
      max_inflight_per_endpoint: 128
    methods:
      /demo.shop.v1.UserService/GetUser:
        idempotent: true
        timeout_millis: 150
        retry:
          enabled: true
          max_attempts: 2
`), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new policy file store: %v", err)
	}
	snapshot, ok := store.Get("user-service")
	if !ok {
		t.Fatalf("expected user-service policy")
	}
	if snapshot.Service != "user-service" || snapshot.RoutingPolicy != "adaptive_p2c" {
		t.Fatalf("unexpected policy identity: %+v", snapshot)
	}
	if snapshot.Retry == nil || !snapshot.Retry.Enabled || snapshot.Retry.MaxAttempts != 2 || snapshot.Retry.BudgetRatio != 0.15 {
		t.Fatalf("unexpected retry policy: %+v", snapshot.Retry)
	}
	if snapshot.OutlierDetection == nil || snapshot.OutlierDetection.EjectThreshold != 2.5 {
		t.Fatalf("unexpected outlier policy: %+v", snapshot.OutlierDetection)
	}
	method := snapshot.Methods["/demo.shop.v1.UserService/GetUser"]
	if method == nil || !method.Idempotent || method.TimeoutMillis != 150 {
		t.Fatalf("unexpected method policy: %+v", method)
	}
}

func TestFileStoreListReturnsSortedClones(t *testing.T) {
	path := t.TempDir() + "/policy.yaml"
	if err := os.WriteFile(path, []byte(`
services:
  z-service:
    retry:
      max_attempts: 3
  a-service:
    retry:
      max_attempts: 1
    outlier_detection:
      eject_threshold: 2.5
    circuit_breaker:
      max_inflight_per_endpoint: 7
    methods:
      /demo.A/Get:
        idempotent: true
        timeout_millis: 15
`), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new policy file store: %v", err)
	}
	listed := store.List()
	if len(listed) != 2 {
		t.Fatalf("expected two policies, got %d", len(listed))
	}
	if listed[0].Service != "a-service" || listed[1].Service != "z-service" {
		t.Fatalf("expected sorted policies by service, got %q then %q", listed[0].Service, listed[1].Service)
	}
	if listed[0].OutlierDetection.GetEjectThreshold() != 2.5 || listed[0].CircuitBreaker.GetMaxInflightPerEndpoint() != 7 {
		t.Fatalf("expected listed policy to include outlier and circuit breaker fields, got %+v", listed[0])
	}

	listed[0].Service = "mutated"
	listed[0].Retry.MaxAttempts = 99
	listed[0].OutlierDetection.EjectThreshold = 9.9
	listed[0].CircuitBreaker.MaxInflightPerEndpoint = 99
	listed[0].Methods["/demo.A/Get"].TimeoutMillis = 999
	listed[0].Methods["/demo.A/Injected"] = nil

	snapshot, ok := store.Get("a-service")
	if !ok {
		t.Fatalf("expected a-service policy")
	}
	if snapshot.Service != "a-service" || snapshot.Retry.GetMaxAttempts() != 1 {
		t.Fatalf("expected stored policy identity and retry to be immutable, got %+v", snapshot)
	}
	if snapshot.OutlierDetection.GetEjectThreshold() != 2.5 || snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() != 7 {
		t.Fatalf("expected stored outlier and circuit breaker fields to be immutable, got %+v", snapshot)
	}
	method := snapshot.Methods["/demo.A/Get"]
	if method == nil || method.TimeoutMillis != 15 {
		t.Fatalf("expected stored method policy to be immutable, got %+v", method)
	}
	if _, ok := snapshot.Methods["/demo.A/Injected"]; ok {
		t.Fatalf("expected injected method on listed clone not to reach store")
	}
}

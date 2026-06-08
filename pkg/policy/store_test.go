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

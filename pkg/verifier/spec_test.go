package verifier

import "testing"

func TestParseSpecReadsVerifierYAML(t *testing.T) {
	spec, err := ParseSpec([]byte(`
test:
  name: canary-user-service
  service: frontend
  method: /frontend.Frontend/GetUserPage
  requests: 1000
expect:
  routes:
    user-service:v1: 0.90
    user-service:v2: 0.10
  tolerance: 0.03
  max_retry_attempts: 1
  forbidden_edges:
    - frontend->payment-service
`))
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	if spec.Test.Name != "canary-user-service" || spec.Test.Requests != 1000 {
		t.Fatalf("unexpected test section: %+v", spec.Test)
	}
	if spec.Expect.Routes["user-service:v2"] != 0.10 {
		t.Fatalf("expected route split for v2, got %+v", spec.Expect.Routes)
	}
	if spec.Expect.MaxRetryAttempts != 1 {
		t.Fatalf("expected max retry attempts 1, got %d", spec.Expect.MaxRetryAttempts)
	}
}

func TestVerifyTraceDistributionPassesWithinTolerance(t *testing.T) {
	spec := Spec{
		Expect: ExpectSpec{
			Routes: map[string]float64{
				"user-service:v1": 0.75,
				"user-service:v2": 0.25,
			},
			Tolerance:        0.01,
			MaxRetryAttempts: 1,
		},
	}

	report := Verify(spec, []TraceRecord{
		{TraceID: "1", Route: "user-service:v1", Path: []string{"frontend", "user-service:v1"}, RetryAttempts: 0},
		{TraceID: "2", Route: "user-service:v1", Path: []string{"frontend", "user-service:v1"}, RetryAttempts: 1},
		{TraceID: "3", Route: "user-service:v1", Path: []string{"frontend", "user-service:v1"}, RetryAttempts: 0},
		{TraceID: "4", Route: "user-service:v2", Path: []string{"frontend", "user-service:v2"}, RetryAttempts: 0},
	})

	if !report.Passed {
		t.Fatalf("expected report to pass, got %+v", report.Checks)
	}
	if report.RouteDistribution["user-service:v2"] != 0.25 {
		t.Fatalf("expected v2 distribution 0.25, got %+v", report.RouteDistribution)
	}
}

func TestVerifyTraceDistributionFailsOutsideToleranceAndForbiddenEdge(t *testing.T) {
	spec := Spec{
		Expect: ExpectSpec{
			Routes: map[string]float64{
				"user-service:v1": 0.50,
				"user-service:v2": 0.50,
			},
			Tolerance:        0.05,
			MaxRetryAttempts: 1,
			ForbiddenEdges:   []string{"frontend->payment-service"},
		},
	}

	report := Verify(spec, []TraceRecord{
		{TraceID: "1", Route: "user-service:v1", Path: []string{"frontend", "payment-service"}, RetryAttempts: 2},
		{TraceID: "2", Route: "user-service:v1", Path: []string{"frontend", "user-service:v1"}, RetryAttempts: 0},
	})

	if report.Passed {
		t.Fatalf("expected report to fail")
	}
	if !report.HasFailedCheck("route_distribution:user-service:v1") {
		t.Fatalf("expected route distribution failure, got %+v", report.Checks)
	}
	if !report.HasFailedCheck("retry_budget") {
		t.Fatalf("expected retry budget failure, got %+v", report.Checks)
	}
	if !report.HasFailedCheck("forbidden_edges") {
		t.Fatalf("expected forbidden edge failure, got %+v", report.Checks)
	}
}

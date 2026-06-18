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
    user-service:primary: 0.90
    user-service:secondary: 0.10
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
	if spec.Expect.Routes["user-service:secondary"] != 0.10 {
		t.Fatalf("expected route split for secondary, got %+v", spec.Expect.Routes)
	}
	if spec.Expect.MaxRetryAttempts != 1 {
		t.Fatalf("expected max retry attempts 1, got %d", spec.Expect.MaxRetryAttempts)
	}
}

func TestVerifyTraceDistributionPassesWithinTolerance(t *testing.T) {
	spec := Spec{
		Expect: ExpectSpec{
			Routes: map[string]float64{
				"user-service:primary":   0.75,
				"user-service:secondary": 0.25,
			},
			Tolerance:        0.01,
			MaxRetryAttempts: 1,
		},
	}

	report := Verify(spec, []TraceRecord{
		{TraceID: "1", Route: "user-service:primary", Path: []string{"frontend", "user-service:primary"}, RetryAttempts: 0},
		{TraceID: "2", Route: "user-service:primary", Path: []string{"frontend", "user-service:primary"}, RetryAttempts: 1},
		{TraceID: "3", Route: "user-service:primary", Path: []string{"frontend", "user-service:primary"}, RetryAttempts: 0},
		{TraceID: "4", Route: "user-service:secondary", Path: []string{"frontend", "user-service:secondary"}, RetryAttempts: 0},
	})

	if !report.Passed {
		t.Fatalf("expected report to pass, got %+v", report.Checks)
	}
	if report.RouteDistribution["user-service:secondary"] != 0.25 {
		t.Fatalf("expected secondary distribution 0.25, got %+v", report.RouteDistribution)
	}
}

func TestVerifyTraceDistributionFailsOutsideToleranceAndForbiddenEdge(t *testing.T) {
	spec := Spec{
		Expect: ExpectSpec{
			Routes: map[string]float64{
				"user-service:primary":   0.50,
				"user-service:secondary": 0.50,
			},
			Tolerance:        0.05,
			MaxRetryAttempts: 1,
			ForbiddenEdges:   []string{"frontend->payment-service"},
		},
	}

	report := Verify(spec, []TraceRecord{
		{TraceID: "1", Route: "user-service:primary", Path: []string{"frontend", "payment-service"}, RetryAttempts: 2},
		{TraceID: "2", Route: "user-service:primary", Path: []string{"frontend", "user-service:primary"}, RetryAttempts: 0},
	})

	if report.Passed {
		t.Fatalf("expected report to fail")
	}
	if !report.HasFailedCheck("route_distribution:user-service:primary") {
		t.Fatalf("expected route distribution failure, got %+v", report.Checks)
	}
	if !report.HasFailedCheck("retry_budget") {
		t.Fatalf("expected retry budget failure, got %+v", report.Checks)
	}
	if !report.HasFailedCheck("forbidden_edges") {
		t.Fatalf("expected forbidden edge failure, got %+v", report.Checks)
	}
}

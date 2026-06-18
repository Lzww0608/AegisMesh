package controller

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
)

func TestPolicyServiceReturnsSnapshot(t *testing.T) {
	service := NewPolicyService(staticPolicyStore{
		snapshot: &aegisv1.PolicySnapshot{
			Service:       "user-service",
			Revision:      7,
			RoutingPolicy: "adaptive_p2c",
			Retry: &aegisv1.RetryPolicy{
				Enabled:       true,
				MaxAttempts:   2,
				BudgetRatio:   0.15,
				MinBudget:     10,
				WindowSeconds: 10,
			},
		},
	}, time.Second)

	resp, err := service.GetPolicy(context.Background(), &aegisv1.GetPolicyRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if resp.Service != "user-service" || resp.Revision != 7 || resp.Retry.GetBudgetRatio() != 0.15 {
		t.Fatalf("unexpected policy snapshot: %+v", resp)
	}
}

func TestPolicyServiceRejectsMissingService(t *testing.T) {
	service := NewPolicyService(staticPolicyStore{}, time.Second)
	_, err := service.GetPolicy(context.Background(), &aegisv1.GetPolicyRequest{})
	if err == nil {
		t.Fatalf("expected missing service to fail")
	}
}

type staticPolicyStore struct {
	snapshot *aegisv1.PolicySnapshot
}

func (s staticPolicyStore) Get(service string) (*aegisv1.PolicySnapshot, bool) {
	if s.snapshot == nil || s.snapshot.Service != service {
		return nil, false
	}
	return s.snapshot, true
}

package aegisgrpc

import (
	"strings"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc/codes"
)

func TestServiceConfigForRoutingPolicySelectsExperimentBalancer(t *testing.T) {
	adaptive, err := serviceConfigForRoutingPolicy(RoutingAdaptiveP2C)
	if err != nil {
		t.Fatalf("adaptive service config: %v", err)
	}
	if !strings.Contains(adaptive, adaptiveP2CBalancerName) {
		t.Fatalf("expected adaptive service config to contain %s, got %s", adaptiveP2CBalancerName, adaptive)
	}

	roundRobin, err := serviceConfigForRoutingPolicy(RoutingRoundRobin)
	if err != nil {
		t.Fatalf("round-robin service config: %v", err)
	}
	if !strings.Contains(roundRobin, "round_robin") {
		t.Fatalf("expected round-robin service config, got %s", roundRobin)
	}
}

func TestRetryModeBuildsBudgetedUnbudgetedAndDisabledPolicies(t *testing.T) {
	defaults := DefaultDialOptions()
	if defaults.RetryMode != RetryBudget {
		t.Fatalf("expected default retry mode to be budget, got %s", defaults.RetryMode)
	}

	budgetedPolicy, budgetedBudget := retryComponentsForDialOptions(defaults)
	if budgetedPolicy.MaxAttempts != 2 || budgetedBudget == nil {
		t.Fatalf("expected budgeted retries with two attempts, got policy=%+v budget=%v", budgetedPolicy, budgetedBudget)
	}

	unbudgeted := defaults
	unbudgeted.RetryMode = RetryWithoutBudget
	withoutBudgetPolicy, withoutBudget := retryComponentsForDialOptions(unbudgeted)
	if withoutBudgetPolicy.MaxAttempts != 2 || withoutBudget != nil {
		t.Fatalf("expected unbudgeted retries with two attempts, got policy=%+v budget=%v", withoutBudgetPolicy, withoutBudget)
	}

	disabled := defaults
	disabled.RetryMode = RetryOff
	disabledPolicy, disabledBudget := retryComponentsForDialOptions(disabled)
	if disabledPolicy.MaxAttempts != 1 || disabledBudget != nil {
		t.Fatalf("expected retries disabled, got policy=%+v budget=%v", disabledPolicy, disabledBudget)
	}
}

func TestDefaultDialOptionsKeepsExistingBudgetShape(t *testing.T) {
	defaults := DefaultDialOptions()
	if defaults.RetryBudget.BudgetRatio != 0.15 || defaults.RetryBudget.MinBudget != 10 || defaults.RetryBudget.Window != 10*time.Second {
		t.Fatalf("unexpected default retry budget: %+v", defaults.RetryBudget)
	}
}

func TestApplyPolicySnapshotOverridesDialOptions(t *testing.T) {
	options := DefaultDialOptions()
	snapshot := &aegisv1.PolicySnapshot{
		RoutingPolicy: string(RoutingRoundRobin),
		Retry: &aegisv1.RetryPolicy{
			Enabled:             true,
			MaxAttempts:         3,
			BudgetRatio:         0.25,
			MinBudget:           4,
			WindowSeconds:       20,
			PerTryTimeoutMillis: 120,
		},
	}

	updated := applyPolicySnapshotToDialOptions(options, snapshot)
	if updated.RoutingPolicy != RoutingRoundRobin {
		t.Fatalf("expected routing policy from snapshot, got %s", updated.RoutingPolicy)
	}
	if updated.RetryMode != RetryBudget || updated.RetryPolicy.MaxAttempts != 3 {
		t.Fatalf("expected budgeted retry policy from snapshot, got mode=%s policy=%+v", updated.RetryMode, updated.RetryPolicy)
	}
	if updated.RetryPolicy.PerTryTimeout != 120*time.Millisecond {
		t.Fatalf("expected per-try timeout from snapshot, got %s", updated.RetryPolicy.PerTryTimeout)
	}
	if updated.RetryBudget.BudgetRatio != 0.25 || updated.RetryBudget.MinBudget != 4 || updated.RetryBudget.Window != 20*time.Second {
		t.Fatalf("expected retry budget from snapshot, got %+v", updated.RetryBudget)
	}
}

func TestDynamicRetrySourceUsesMethodPolicy(t *testing.T) {
	source := newDynamicRetrySource(DefaultDialOptions(), &policyManager{})
	source.Update(&aegisv1.PolicySnapshot{
		Retry: &aegisv1.RetryPolicy{
			Enabled:             true,
			MaxAttempts:         3,
			BudgetRatio:         0.5,
			MinBudget:           1,
			WindowSeconds:       10,
			PerTryTimeoutMillis: 500,
		},
		Methods: map[string]*aegisv1.MethodPolicy{
			"/demo.shop.v1.OrderService/CreateOrder": {
				Method:        "/demo.shop.v1.OrderService/CreateOrder",
				Idempotent:    false,
				TimeoutMillis: 300,
				Retry: &aegisv1.RetryPolicy{
					Enabled:     false,
					MaxAttempts: 1,
				},
			},
			"/demo.shop.v1.UserService/GetUser": {
				Method:        "/demo.shop.v1.UserService/GetUser",
				Idempotent:    true,
				TimeoutMillis: 150,
				Retry: &aegisv1.RetryPolicy{
					Enabled:             true,
					MaxAttempts:         2,
					PerTryTimeoutMillis: 100,
				},
			},
		},
	})

	orderPolicy, orderBudget := source.PolicyForMethod("/demo.shop.v1.OrderService/CreateOrder")
	if orderPolicy.MaxAttempts != 1 || orderBudget != nil {
		t.Fatalf("expected non-idempotent CreateOrder retry disabled, got policy=%+v budget=%v", orderPolicy, orderBudget)
	}
	if orderPolicy.PerTryTimeout != 300*time.Millisecond {
		t.Fatalf("expected method timeout override, got %s", orderPolicy.PerTryTimeout)
	}

	userPolicy, userBudget := source.PolicyForMethod("/demo.shop.v1.UserService/GetUser")
	if userPolicy.MaxAttempts != 2 || userPolicy.PerTryTimeout != 100*time.Millisecond || userBudget == nil {
		t.Fatalf("expected method retry override with budget, got policy=%+v budget=%v", userPolicy, userBudget)
	}
}

func TestPolicyRetryToSDKPolicyKeepsRetryableCodes(t *testing.T) {
	options := DefaultDialOptions()
	updated := applyPolicySnapshotToDialOptions(options, &aegisv1.PolicySnapshot{
		Retry: &aegisv1.RetryPolicy{Enabled: true, MaxAttempts: 4},
	})

	if updated.RetryPolicy.MaxAttempts != 4 {
		t.Fatalf("expected max attempts from policy, got %d", updated.RetryPolicy.MaxAttempts)
	}
	if len(updated.RetryPolicy.RetryableCodes) == 0 || updated.RetryPolicy.RetryableCodes[0] != codes.Unavailable {
		t.Fatalf("expected retryable codes to be preserved, got %+v", updated.RetryPolicy.RetryableCodes)
	}
}

func TestDynamicRetrySourceRebuildsBudgetOnRevisionChange(t *testing.T) {
	source := newDynamicRetrySource(DialOptions{
		RoutingPolicy: RoutingAdaptiveP2C,
		RetryMode:     RetryBudget,
		RetryPolicy:   RetryPolicy{MaxAttempts: 2, RetryableCodes: []codes.Code{codes.Unavailable}},
		RetryBudget: retrypkg.BudgetConfig{
			BudgetRatio: 0,
			MinBudget:   0,
			Window:      time.Minute,
		},
	}, &policyManager{})
	source.Update(&aegisv1.PolicySnapshot{Revision: 1})
	_, budget := source.PolicyForMethod("/demo.shop.v1.UserService/GetUser")
	if budget == nil || budget.AllowRetry() {
		t.Fatalf("expected initial exhausted zero budget")
	}

	source.Update(&aegisv1.PolicySnapshot{
		Revision: 2,
		Retry: &aegisv1.RetryPolicy{
			Enabled:       true,
			MaxAttempts:   2,
			BudgetRatio:   1,
			MinBudget:     1,
			WindowSeconds: 60,
		},
	})
	_, budget = source.PolicyForMethod("/demo.shop.v1.UserService/GetUser")
	if budget == nil || !budget.AllowRetry() {
		t.Fatalf("expected rebuilt budget to allow retry")
	}
}

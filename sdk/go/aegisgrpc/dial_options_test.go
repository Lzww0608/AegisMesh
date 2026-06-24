package aegisgrpc

import (
	"strings"
	"sync"
	"sync/atomic"
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
	if orderPolicy.maxAttempts != 1 || orderBudget != nil {
		t.Fatalf("expected non-idempotent CreateOrder retry disabled, got policy=%+v budget=%v", orderPolicy, orderBudget)
	}
	if orderPolicy.perTryTimeout != 300*time.Millisecond {
		t.Fatalf("expected method timeout override, got %s", orderPolicy.perTryTimeout)
	}

	userPolicy, userBudget := source.PolicyForMethod("/demo.shop.v1.UserService/GetUser")
	if userPolicy.maxAttempts != 2 || userPolicy.perTryTimeout != 100*time.Millisecond || userBudget == nil {
		t.Fatalf("expected method retry override with budget, got policy=%+v budget=%v", userPolicy, userBudget)
	}
}

func TestDynamicRetrySourcePolicyForMethodIsAllocationFree(t *testing.T) {
	const method = "/demo.shop.v1.UserService/GetUser"
	source := newDynamicRetrySource(DialOptions{
		RoutingPolicy: RoutingAdaptiveP2C,
		RetryMode:     RetryBudget,
		RetryPolicy: RetryPolicy{
			MaxAttempts:    2,
			PerTryTimeout:  750 * time.Millisecond,
			RetryableCodes: []codes.Code{codes.Unavailable, codes.DeadlineExceeded},
		},
		RetryBudget: retrypkg.BudgetConfig{
			BudgetRatio: 0.15,
			MinBudget:   10,
			Window:      10 * time.Second,
		},
	}, &policyManager{})
	source.Update(&aegisv1.PolicySnapshot{
		Revision:      7,
		RoutingPolicy: string(RoutingRoundRobin),
		Retry: &aegisv1.RetryPolicy{
			Enabled:             true,
			MaxAttempts:         3,
			BudgetRatio:         0.5,
			MinBudget:           1,
			WindowSeconds:       10,
			PerTryTimeoutMillis: 500,
		},
		Methods: map[string]*aegisv1.MethodPolicy{
			method: {
				Method:        method,
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
	source.PolicyForMethod(method)

	allocs := testing.AllocsPerRun(1000, func() {
		policy, budget := source.PolicyForMethod(method)
		if policy.maxAttempts != 2 || policy.perTryTimeout != 100*time.Millisecond || budget == nil {
			t.Fatalf("unexpected method policy: policy=%+v budget=%v", policy, budget)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected PolicyForMethod hot path to allocate 0 times, got %.2f", allocs)
	}
}

func TestDynamicRetrySourceUsesImmutableCompiledSnapshot(t *testing.T) {
	const method = "/demo.shop.v1.UserService/GetUser"
	snapshot := &aegisv1.PolicySnapshot{
		Revision: 1,
		Retry: &aegisv1.RetryPolicy{
			Enabled:             true,
			MaxAttempts:         3,
			BudgetRatio:         0.5,
			MinBudget:           1,
			WindowSeconds:       10,
			PerTryTimeoutMillis: 500,
		},
		Methods: map[string]*aegisv1.MethodPolicy{
			method: {
				Method:        method,
				Idempotent:    true,
				TimeoutMillis: 150,
			},
		},
	}
	source := newDynamicRetrySource(DefaultDialOptions(), &policyManager{})
	source.Update(snapshot)

	snapshot.Retry.MaxAttempts = 9
	snapshot.Methods[method].TimeoutMillis = 900
	snapshot.Methods[method] = &aegisv1.MethodPolicy{
		Method:        method,
		Idempotent:    false,
		TimeoutMillis: 250,
	}

	policy, budget := source.PolicyForMethod(method)
	if policy.maxAttempts != 3 || policy.perTryTimeout != 150*time.Millisecond || budget == nil {
		t.Fatalf("expected compiled snapshot to ignore post-update proto mutation, got policy=%+v budget=%v", policy, budget)
	}

	source.Update(&aegisv1.PolicySnapshot{
		Revision: 2,
		Methods: map[string]*aegisv1.MethodPolicy{
			method: {
				Method:        method,
				Idempotent:    false,
				TimeoutMillis: 250,
			},
		},
	})
	policy, budget = source.PolicyForMethod(method)
	if policy.maxAttempts != 1 || policy.perTryTimeout != 250*time.Millisecond || budget != nil {
		t.Fatalf("expected latest compiled snapshot to disable retries, got policy=%+v budget=%v", policy, budget)
	}
}

func TestDynamicRetrySourceMethodBudgetsDoNotOversubscribeConcurrent(t *testing.T) {
	methods := []string{
		"/demo.shop.v1.UserService/GetUser",
		"/demo.shop.v1.OrderService/GetOrder",
	}
	source := newDynamicRetrySource(DialOptions{
		RoutingPolicy: RoutingAdaptiveP2C,
		RetryMode:     RetryBudget,
		RetryPolicy:   RetryPolicy{MaxAttempts: 2, RetryableCodes: []codes.Code{codes.Unavailable}},
		RetryBudget: retrypkg.BudgetConfig{
			BudgetRatio: 0.15,
			MinBudget:   0,
			Window:      time.Minute,
		},
	}, &policyManager{})
	source.Update(&aegisv1.PolicySnapshot{
		Revision: 1,
		Retry: &aegisv1.RetryPolicy{
			Enabled:       true,
			MaxAttempts:   2,
			BudgetRatio:   0.15,
			WindowSeconds: 60,
		},
		Methods: map[string]*aegisv1.MethodPolicy{
			methods[0]: {Method: methods[0], Idempotent: true},
			methods[1]: {Method: methods[1], Idempotent: true},
		},
	})

	for _, method := range methods {
		_, budget := source.PolicyForMethod(method)
		if budget == nil {
			t.Fatalf("expected budget for %s", method)
		}
		for i := 0; i < 100; i++ {
			budget.RecordOriginal()
		}

		if got := acquireRetryBudgetConcurrently(budget, 100); got != 15 {
			t.Fatalf("expected exactly 15 retry acquisitions for %s, got %d", method, got)
		}
	}
}

func acquireRetryBudgetConcurrently(budget *retrypkg.Budget, goroutines int) int64 {
	var successes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if budget.TryAcquireRetry() {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	return successes.Load()
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
	if budget == nil || budget.TryAcquireRetry() {
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
	if budget == nil || !budget.TryAcquireRetry() {
		t.Fatalf("expected rebuilt budget to allow retry")
	}
}

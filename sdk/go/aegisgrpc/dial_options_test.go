package aegisgrpc

import (
	"strings"
	"testing"
	"time"
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

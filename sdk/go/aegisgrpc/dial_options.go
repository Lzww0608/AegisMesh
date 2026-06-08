package aegisgrpc

import (
	"fmt"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
)

type RoutingPolicy string

const (
	RoutingAdaptiveP2C RoutingPolicy = "adaptive_p2c"
	RoutingRoundRobin  RoutingPolicy = "round_robin"
)

type RetryMode string

const (
	RetryBudget        RetryMode = "budget"
	RetryWithoutBudget RetryMode = "without_budget"
	RetryOff           RetryMode = "off"
)

type DialOptions struct {
	RoutingPolicy    RoutingPolicy
	RetryMode        RetryMode
	RetryPolicy      RetryPolicy
	RetryBudget      retrypkg.BudgetConfig
	DisableTelemetry bool
	DisablePolicy    bool
	TraceLogPath     string
}

func DefaultDialOptions() DialOptions {
	return DialOptions{
		RoutingPolicy: RoutingAdaptiveP2C,
		RetryMode:     RetryBudget,
		RetryPolicy:   defaultRetryPolicy(),
		RetryBudget: retrypkg.BudgetConfig{
			BudgetRatio: 0.15,
			MinBudget:   10,
			Window:      10 * time.Second,
		},
	}
}

func normalizeDialOptions(options DialOptions) DialOptions {
	defaults := DefaultDialOptions()
	if options.RoutingPolicy == "" {
		options.RoutingPolicy = defaults.RoutingPolicy
	}
	if options.RetryMode == "" {
		options.RetryMode = defaults.RetryMode
	}
	if options.RetryPolicy.MaxAttempts == 0 {
		options.RetryPolicy = defaults.RetryPolicy
	}
	if isZeroBudgetConfig(options.RetryBudget) {
		options.RetryBudget = defaults.RetryBudget
	}
	return options
}

func isZeroBudgetConfig(cfg retrypkg.BudgetConfig) bool {
	return cfg.BudgetRatio == 0 && cfg.MinBudget == 0 && cfg.Window == 0 && cfg.Now == nil
}

func serviceConfigForRoutingPolicy(policy RoutingPolicy) (string, error) {
	switch policy {
	case "", RoutingAdaptiveP2C:
		return adaptiveP2CServiceConfig, nil
	case RoutingRoundRobin:
		return roundRobinServiceConfig, nil
	default:
		return "", fmt.Errorf("unsupported routing policy %q", policy)
	}
}

func retryComponentsForDialOptions(options DialOptions) (RetryPolicy, *retrypkg.Budget) {
	options = normalizeDialOptions(options)
	policy := options.RetryPolicy

	switch options.RetryMode {
	case "", RetryBudget:
		return policy, retrypkg.NewBudget(options.RetryBudget)
	case RetryWithoutBudget:
		return policy, nil
	case RetryOff:
		policy.MaxAttempts = 1
		return policy, nil
	default:
		return policy, retrypkg.NewBudget(options.RetryBudget)
	}
}

func applyPolicySnapshotToDialOptions(options DialOptions, snapshot *aegisv1.PolicySnapshot) DialOptions {
	if snapshot == nil {
		return options
	}
	if snapshot.RoutingPolicy != "" {
		options.RoutingPolicy = RoutingPolicy(snapshot.RoutingPolicy)
	}
	return applyRetryPolicyToDialOptions(options, snapshot.Retry)
}

func applyMethodPolicyToDialOptions(options DialOptions, method *aegisv1.MethodPolicy) DialOptions {
	if method == nil {
		return options
	}
	if method.TimeoutMillis > 0 {
		options.RetryPolicy.PerTryTimeout = time.Duration(method.TimeoutMillis) * time.Millisecond
	}
	if policyRetryHasAnyField(method.Retry) {
		return applyRetryPolicyToDialOptions(options, method.Retry)
	}
	if !method.Idempotent {
		options.RetryMode = RetryOff
		options.RetryPolicy.MaxAttempts = 1
	}
	return options
}

func applyRetryPolicyToDialOptions(options DialOptions, policy *aegisv1.RetryPolicy) DialOptions {
	if !policyRetryHasAnyField(policy) {
		return options
	}
	if !policy.Enabled {
		options.RetryMode = RetryOff
		options.RetryPolicy.MaxAttempts = 1
		return options
	}

	options.RetryMode = RetryBudget
	if policy.MaxAttempts > 0 {
		options.RetryPolicy.MaxAttempts = int(policy.MaxAttempts)
	}
	if policy.PerTryTimeoutMillis > 0 {
		options.RetryPolicy.PerTryTimeout = time.Duration(policy.PerTryTimeoutMillis) * time.Millisecond
	}
	if policy.BudgetRatio > 0 {
		options.RetryBudget.BudgetRatio = policy.BudgetRatio
	}
	if policy.MinBudget > 0 {
		options.RetryBudget.MinBudget = int64(policy.MinBudget)
	}
	if policy.WindowSeconds > 0 {
		options.RetryBudget.Window = time.Duration(policy.WindowSeconds) * time.Second
	}
	return options
}

func policyRetryHasAnyField(policy *aegisv1.RetryPolicy) bool {
	return policy != nil &&
		(policy.Enabled ||
			policy.MaxAttempts != 0 ||
			policy.BudgetRatio != 0 ||
			policy.MinBudget != 0 ||
			policy.WindowSeconds != 0 ||
			policy.PerTryTimeoutMillis != 0)
}

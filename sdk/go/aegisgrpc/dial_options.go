package aegisgrpc

import (
	"fmt"
	"time"

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

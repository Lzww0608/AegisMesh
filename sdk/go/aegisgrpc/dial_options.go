package aegisgrpc

import (
	"fmt"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc/credentials"
)

// RoutingPolicy describes routing policy rules distributed through the control plane.
type RoutingPolicy string

const (
	// RoutingAdaptiveP2C identifies the routing adaptive p2 c constant used by this package.
	RoutingAdaptiveP2C RoutingPolicy = "adaptive_p2c"
	RoutingRoundRobin  RoutingPolicy = "round_robin"
)

// RetryMode names the retry mode values accepted by resolver, picker, and reporter state.
type RetryMode string

const (
	// RetryBudget identifies the retry budget constant used by this package.
	RetryBudget        RetryMode = "budget"
	RetryWithoutBudget RetryMode = "without_budget"
	RetryOff           RetryMode = "off"
)

// DialOptions holds optional settings for dial options operations.
type DialOptions struct {
	RoutingPolicy        RoutingPolicy
	RetryMode            RetryMode
	RetryPolicy          RetryPolicy
	RetryBudget          retrypkg.BudgetConfig
	TransportCredentials credentials.TransportCredentials
	ControllerSecurity   security.ClientConfig
	ControllerAddrs      []string
	DisableTelemetry     bool
	DisablePolicy        bool
	TraceLogPath         string
}

// DefaultDialOptions keeps default dial options rules consistent for resolver, picker, and reporter state.
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

// normalizeDialOptions normalizes normalize dial options so downstream logic sees one canonical form.
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

// isZeroBudgetConfig provides the shared is zero budget config helper for resolver, picker, and reporter state.
func isZeroBudgetConfig(cfg retrypkg.BudgetConfig) bool {
	return cfg.BudgetRatio == 0 && cfg.MinBudget == 0 && cfg.Window == 0 && cfg.Now == nil
}

// serviceConfigForRoutingPolicy provides the shared service config for routing policy helper for resolver, picker, and reporter state.
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

// retryComponentsForDialOptions provides the shared retry components for dial options helper for resolver, picker, and reporter state.
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

// applyPolicySnapshotToDialOptions applies apply policy snapshot to dial options to the mutable target while preserving transition rules.
func applyPolicySnapshotToDialOptions(options DialOptions, snapshot *aegisv1.PolicySnapshot) DialOptions {
	if snapshot == nil {
		return options
	}
	if snapshot.RoutingPolicy != "" {
		options.RoutingPolicy = RoutingPolicy(snapshot.RoutingPolicy)
	}
	return applyRetryPolicyToDialOptions(options, snapshot.Retry)
}

// applyMethodPolicyToDialOptions applies apply method policy to dial options to the mutable target while preserving transition rules.
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

// applyRetryPolicyToDialOptions applies apply retry policy to dial options to the mutable target while preserving transition rules.
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

// policyRetryHasAnyField provides the shared policy retry has any field helper for resolver, picker, and reporter state.
func policyRetryHasAnyField(policy *aegisv1.RetryPolicy) bool {
	return policy != nil &&
		(policy.Enabled ||
			policy.MaxAttempts != 0 ||
			policy.BudgetRatio != 0 ||
			policy.MinBudget != 0 ||
			policy.WindowSeconds != 0 ||
			policy.PerTryTimeoutMillis != 0)
}

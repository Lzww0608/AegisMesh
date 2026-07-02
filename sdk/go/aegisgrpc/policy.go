package aegisgrpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultPolicyFetchTimeout = 500 * time.Millisecond
	defaultPolicyWatchBackoff = 2 * time.Second
)

type circuitBreakerPolicyApplier interface {
	SetMaxInflightPerEndpoint(max int64)
}

type policyManager struct {
	v              atomic.Value // stores *compiledPolicy
	circuitBreaker circuitBreakerPolicyApplier
}

func (m *policyManager) Update(snapshot *aegisv1.PolicySnapshot) {
	if m == nil || snapshot == nil {
		return
	}
	policy := compilePolicySnapshot(snapshot)
	m.v.Store(policy)
	if m.circuitBreaker != nil {
		m.circuitBreaker.SetMaxInflightPerEndpoint(policy.circuitBreaker.maxInflightPerEndpoint)
	}
}

func (m *policyManager) Load() *compiledPolicy {
	if m == nil {
		return nil
	}
	policy, _ := m.v.Load().(*compiledPolicy)
	return policy
}

type compiledPolicy struct {
	version        int64
	routing        RoutingPolicy
	defaultRetry   compiledRetryPatch
	circuitBreaker compiledCircuitBreakerPolicy
	methods        map[string]compiledMethod
}

type compiledCircuitBreakerPolicy struct {
	maxInflightPerEndpoint int64
}

type compiledMethod struct {
	idempotent bool
	timeout    time.Duration
	retry      compiledRetryPatch
}

type compiledRetryPatch struct {
	has           bool
	enabled       bool
	maxAttempts   int
	perTryTimeout time.Duration
	budgetRatio   float64
	minBudget     int64
	window        time.Duration
}

func compilePolicySnapshot(snapshot *aegisv1.PolicySnapshot) *compiledPolicy {
	policy := &compiledPolicy{
		version:        snapshot.Revision,
		routing:        RoutingPolicy(snapshot.RoutingPolicy),
		defaultRetry:   compileRetryPatch(snapshot.Retry),
		circuitBreaker: compileCircuitBreakerPolicy(snapshot.CircuitBreaker),
	}
	if len(snapshot.Methods) == 0 {
		return policy
	}

	methods := make(map[string]compiledMethod, len(snapshot.Methods))
	for methodName, method := range snapshot.Methods {
		if method == nil {
			continue
		}
		key := methodName
		if key == "" {
			key = method.Method
		}
		methods[key] = compiledMethod{
			idempotent: method.Idempotent,
			timeout:    durationMillis(method.TimeoutMillis),
			retry:      compileRetryPatch(method.Retry),
		}
	}
	policy.methods = methods
	return policy
}

func compileCircuitBreakerPolicy(policy *aegisv1.CircuitBreakerPolicy) compiledCircuitBreakerPolicy {
	max := adaptiveDefaultMaxInflightPerTarget
	if policy != nil && policy.MaxInflightPerEndpoint > 0 {
		max = policy.MaxInflightPerEndpoint
	}
	return compiledCircuitBreakerPolicy{maxInflightPerEndpoint: max}
}
func compileRetryPatch(policy *aegisv1.RetryPolicy) compiledRetryPatch {
	if !policyRetryHasAnyField(policy) {
		return compiledRetryPatch{}
	}
	return compiledRetryPatch{
		has:           true,
		enabled:       policy.Enabled,
		maxAttempts:   int(policy.MaxAttempts),
		perTryTimeout: durationMillis(policy.PerTryTimeoutMillis),
		budgetRatio:   policy.BudgetRatio,
		minBudget:     int64(policy.MinBudget),
		window:        durationSeconds(policy.WindowSeconds),
	}
}

func compileRetryFromDialOptions(options DialOptions) compiledRetry {
	options = normalizeDialOptions(options)
	retry := compileRetryPolicy(options.RetryPolicy)
	retry.mode = options.RetryMode
	retry.budget = options.RetryBudget
	if retry.mode == RetryOff {
		retry.maxAttempts = 1
	}
	return retry
}

func applyCompiledRetryPatch(retry compiledRetry, patch compiledRetryPatch) compiledRetry {
	if !patch.has {
		return retry
	}
	if !patch.enabled {
		retry.mode = RetryOff
		retry.maxAttempts = 1
		return retry
	}

	retry.mode = RetryBudget
	if patch.maxAttempts > 0 {
		retry.maxAttempts = patch.maxAttempts
	}
	if patch.perTryTimeout > 0 {
		retry.perTryTimeout = patch.perTryTimeout
	}
	if patch.budgetRatio > 0 {
		retry.budget.BudgetRatio = patch.budgetRatio
	}
	if patch.minBudget > 0 {
		retry.budget.MinBudget = patch.minBudget
	}
	if patch.window > 0 {
		retry.budget.Window = patch.window
	}
	return retry
}

func applyCompiledMethod(retry compiledRetry, method compiledMethod) compiledRetry {
	if method.timeout > 0 {
		retry.perTryTimeout = method.timeout
	}
	if method.retry.has {
		return applyCompiledRetryPatch(retry, method.retry)
	}
	if !method.idempotent {
		retry.mode = RetryOff
		retry.maxAttempts = 1
	}
	return retry
}

func durationMillis(ms int64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func durationSeconds(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

type dynamicRetrySource struct {
	mu              sync.Mutex
	defaults        compiledRetry
	manager         *policyManager
	budgets         map[string]*retrypkg.Budget
	budgetRevisions map[string]int64
}

func newDynamicRetrySource(defaults DialOptions, manager *policyManager) *dynamicRetrySource {
	if manager == nil {
		manager = &policyManager{}
	}
	return &dynamicRetrySource{
		defaults:        compileRetryFromDialOptions(defaults),
		manager:         manager,
		budgets:         make(map[string]*retrypkg.Budget),
		budgetRevisions: make(map[string]int64),
	}
}

func (s *dynamicRetrySource) Update(snapshot *aegisv1.PolicySnapshot) {
	s.manager.Update(snapshot)
}

func (s *dynamicRetrySource) PolicyForMethod(method string) (compiledRetry, *retrypkg.Budget) {
	retry := s.defaults
	revision := int64(0)
	if policy := s.manager.Load(); policy != nil {
		revision = policy.version
		retry = applyCompiledRetryPatch(retry, policy.defaultRetry)
		if methodPolicy, ok := policy.methods[method]; ok {
			retry = applyCompiledMethod(retry, methodPolicy)
		}
	}

	if retry.mode == RetryOff || retry.mode == RetryWithoutBudget {
		return retry, nil
	}
	return retry, s.budgetForMethod(method, revision, retry.budget)
}

func (s *dynamicRetrySource) budgetForMethod(method string, revision int64, cfg retrypkg.BudgetConfig) *retrypkg.Budget {
	s.mu.Lock()
	defer s.mu.Unlock()

	budget := s.budgets[method]
	if budget == nil || s.budgetRevisions[method] != revision {
		budget = retrypkg.NewBudget(cfg)
		s.budgets[method] = budget
		s.budgetRevisions[method] = revision
	}
	return budget
}

func retryPolicyAndBudgetConfig(options DialOptions) (RetryPolicy, retrypkg.BudgetConfig) {
	retry := compileRetryFromDialOptions(options)
	return retry.toPolicy(), retry.budget
}

func loadInitialPolicy(ctx context.Context, controllerAddr, service string, manager *policyManager, dialOptions []grpc.DialOption) *aegisv1.PolicySnapshot {
	policyCtx, cancel := context.WithTimeout(ctx, defaultPolicyFetchTimeout)
	defer cancel()

	conn, err := grpc.DialContext(policyCtx, controllerAddr, dialOptions...)
	if err != nil {
		return nil
	}
	defer conn.Close()

	snapshot, err := aegisv1.NewPolicyServiceClient(conn).GetPolicy(policyCtx, &aegisv1.GetPolicyRequest{Service: service})
	if err != nil {
		return nil
	}
	manager.Update(snapshot)
	return snapshot
}

func startPolicyWatcher(ctx context.Context, controllerAddr, service string, manager *policyManager, dialOptions []grpc.DialOption) {
	go func() {
		for ctx.Err() == nil {
			err := watchPolicyOnce(ctx, controllerAddr, service, manager, dialOptions)
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			timer := time.NewTimer(defaultPolicyWatchBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func watchPolicyOnce(ctx context.Context, controllerAddr, service string, manager *policyManager, dialOptions []grpc.DialOption) error {
	conn, err := grpc.DialContext(ctx, controllerAddr, dialOptions...)
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := aegisv1.NewPolicyServiceClient(conn).WatchPolicy(ctx, &aegisv1.WatchPolicyRequest{Service: service})
	if err != nil {
		if status.Code(err) == codes.Unimplemented || status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			return err
		}
		manager.Update(snapshot)
	}
}

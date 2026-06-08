package aegisgrpc

import (
	"context"
	"errors"
	"sync"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	defaultPolicyFetchTimeout = 500 * time.Millisecond
	defaultPolicyWatchBackoff = 2 * time.Second
)

type policyManager struct {
	mu       sync.RWMutex
	snapshot *aegisv1.PolicySnapshot
}

func (m *policyManager) Update(snapshot *aegisv1.PolicySnapshot) {
	if snapshot == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = proto.Clone(snapshot).(*aegisv1.PolicySnapshot)
}

func (m *policyManager) Snapshot() *aegisv1.PolicySnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snapshot == nil {
		return nil
	}
	return proto.Clone(m.snapshot).(*aegisv1.PolicySnapshot)
}

type dynamicRetrySource struct {
	mu             sync.Mutex
	defaults       DialOptions
	manager        *policyManager
	budgets        map[string]*retrypkg.Budget
	budgetVersions map[string]int64
}

func newDynamicRetrySource(defaults DialOptions, manager *policyManager) *dynamicRetrySource {
	if manager == nil {
		manager = &policyManager{}
	}
	return &dynamicRetrySource{
		defaults:       normalizeDialOptions(defaults),
		manager:        manager,
		budgets:        make(map[string]*retrypkg.Budget),
		budgetVersions: make(map[string]int64),
	}
}

func (s *dynamicRetrySource) Update(snapshot *aegisv1.PolicySnapshot) {
	s.manager.Update(snapshot)
}

func (s *dynamicRetrySource) PolicyForMethod(method string) (RetryPolicy, *retrypkg.Budget) {
	snapshot := s.manager.Snapshot()
	options := applyPolicySnapshotToDialOptions(s.defaults, snapshot)
	if snapshot != nil {
		options = applyMethodPolicyToDialOptions(options, snapshot.Methods[method])
	}

	policy, budgetCfg := retryPolicyAndBudgetConfig(options)
	if options.RetryMode == RetryOff {
		return policy, nil
	}
	if options.RetryMode == RetryWithoutBudget {
		return policy, nil
	}

	version := int64(0)
	if snapshot != nil {
		version = snapshot.Version
	}
	budget := s.budgetForMethod(method, version, budgetCfg)
	return policy, budget
}

func (s *dynamicRetrySource) budgetForMethod(method string, version int64, cfg retrypkg.BudgetConfig) *retrypkg.Budget {
	s.mu.Lock()
	defer s.mu.Unlock()

	budget := s.budgets[method]
	if budget == nil || s.budgetVersions[method] != version {
		budget = retrypkg.NewBudget(cfg)
		s.budgets[method] = budget
		s.budgetVersions[method] = version
	}
	return budget
}

func retryPolicyAndBudgetConfig(options DialOptions) (RetryPolicy, retrypkg.BudgetConfig) {
	options = normalizeDialOptions(options)
	policy := options.RetryPolicy
	if options.RetryMode == RetryOff {
		policy.MaxAttempts = 1
	}
	return policy, options.RetryBudget
}

func loadInitialPolicy(ctx context.Context, controllerAddr, service string, manager *policyManager) *aegisv1.PolicySnapshot {
	policyCtx, cancel := context.WithTimeout(ctx, defaultPolicyFetchTimeout)
	defer cancel()

	conn, err := grpc.DialContext(policyCtx, controllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

func startPolicyWatcher(ctx context.Context, controllerAddr, service string, manager *policyManager) {
	go func() {
		for ctx.Err() == nil {
			err := watchPolicyOnce(ctx, controllerAddr, service, manager)
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

func watchPolicyOnce(ctx context.Context, controllerAddr, service string, manager *policyManager) error {
	conn, err := grpc.DialContext(ctx, controllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

package controller

import (
	"context"
	"os"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/policy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

func TestPolicyServiceSendsTombstoneWhenWatchedPolicyIsRemoved(t *testing.T) {
	store := &mutablePolicyStore{snapshot: &aegisv1.PolicySnapshot{Service: "user-service", Revision: 7}}
	service := NewPolicyService(store, time.Second)
	stream := &capturePolicyStream{ctx: context.Background()}
	var lastRevision int64

	if err := service.sendIfChanged("user-service", &lastRevision, stream); err != nil {
		t.Fatalf("send initial policy: %v", err)
	}
	if lastRevision != 7 || len(stream.sent) != 1 || stream.sent[0].Revision != 7 {
		t.Fatalf("expected initial policy revision 7, last=%d sent=%+v", lastRevision, stream.sent)
	}

	store.snapshot = nil
	if err := service.sendIfChanged("user-service", &lastRevision, stream); err != nil {
		t.Fatalf("send tombstone: %v", err)
	}
	if lastRevision != deletedPolicyRevision || len(stream.sent) != 2 {
		t.Fatalf("expected one tombstone, last=%d sent=%+v", lastRevision, stream.sent)
	}
	tombstone := stream.sent[1]
	if tombstone.Service != "user-service" || tombstone.Revision != deletedPolicyRevision || tombstone.Retry != nil || tombstone.CircuitBreaker != nil {
		t.Fatalf("unexpected tombstone snapshot: %+v", tombstone)
	}

	if err := service.sendIfChanged("user-service", &lastRevision, stream); err != nil {
		t.Fatalf("repeat tombstone check: %v", err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("expected tombstone to be sent only once, got %d snapshots", len(stream.sent))
	}
}

func TestPolicyServiceReturnsNotFoundWhenWatchStartsWithoutPolicy(t *testing.T) {
	service := NewPolicyService(&mutablePolicyStore{}, time.Second)
	stream := &capturePolicyStream{ctx: context.Background()}
	var lastRevision int64

	err := service.sendIfChanged("user-service", &lastRevision, stream)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for initially missing policy, got %v", err)
	}
	if len(stream.sent) != 0 {
		t.Fatalf("expected no snapshots for initially missing policy, got %+v", stream.sent)
	}
}

type mutablePolicyStore struct {
	snapshot *aegisv1.PolicySnapshot
}

func (s *mutablePolicyStore) Get(service string) (*aegisv1.PolicySnapshot, bool) {
	if s.snapshot == nil || s.snapshot.Service != service {
		return nil, false
	}
	return s.snapshot, true
}

type capturePolicyStream struct {
	ctx  context.Context
	sent []*aegisv1.PolicySnapshot
}

func (s *capturePolicyStream) Send(snapshot *aegisv1.PolicySnapshot) error {
	s.sent = append(s.sent, snapshot)
	return nil
}

func (s *capturePolicyStream) SetHeader(metadata.MD) error  { return nil }
func (s *capturePolicyStream) SendHeader(metadata.MD) error { return nil }
func (s *capturePolicyStream) SetTrailer(metadata.MD)       {}
func (s *capturePolicyStream) Context() context.Context     { return s.ctx }
func (s *capturePolicyStream) SendMsg(any) error            { return nil }
func (s *capturePolicyStream) RecvMsg(any) error            { return nil }
func TestOutlierDetectionToStateMachineConfig(t *testing.T) {
	cfg := OutlierDetectionToStateMachineConfig(&aegisv1.OutlierDetectionPolicy{
		DegradedThreshold:       1.2,
		EjectThreshold:          2.4,
		ConsecutiveWindows:      4,
		EjectionDurationSeconds: 9,
		RecoveryThreshold:       0.7,
		ProbeSuccessThreshold:   0.93,
	})

	if cfg.DegradedThreshold != 1.2 || cfg.EjectThreshold != 2.4 || cfg.ConsecutiveWindows != 4 {
		t.Fatalf("unexpected threshold/window conversion: %+v", cfg)
	}
	if cfg.EjectionDuration != 9*time.Second || cfg.RecoveryThreshold != 0.7 || cfg.ProbeSuccessThreshold != 0.93 {
		t.Fatalf("unexpected duration/recovery conversion: %+v", cfg)
	}

	zero := OutlierDetectionToStateMachineConfig(nil)
	if zero != (fault.StateMachineConfig{}) {
		t.Fatalf("expected nil outlier policy to produce zero-value override, got %+v", zero)
	}
}

func TestApplyOutlierDetectionPoliciesReplacesServiceConfigs(t *testing.T) {
	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		StateMachine: fault.StateMachineConfig{
			DegradedThreshold:     9,
			EjectThreshold:        10,
			ConsecutiveWindows:    5,
			EjectionDuration:      30 * time.Second,
			RecoveryThreshold:     4,
			ProbeSuccessThreshold: 0.7,
		},
	})
	store := staticPolicyListStore{snapshots: []*aegisv1.PolicySnapshot{
		{
			Service: "user-service",
			OutlierDetection: &aegisv1.OutlierDetectionPolicy{
				DegradedThreshold:       1,
				EjectionDurationSeconds: 7,
			},
		},
	}}

	changed := ApplyOutlierDetectionPolicies(store, manager)
	if changed != 1 {
		t.Fatalf("expected one changed service config, got %d", changed)
	}
	cfg := manager.EffectiveStateMachineConfig("user-service")
	if cfg.DegradedThreshold != 1 || cfg.EjectThreshold != 10 || cfg.ConsecutiveWindows != 5 || cfg.EjectionDuration != 7*time.Second {
		t.Fatalf("expected policy values with base inheritance, got %+v", cfg)
	}

	store.snapshots = nil
	changed = ApplyOutlierDetectionPolicies(store, manager)
	if changed != 1 {
		t.Fatalf("expected removing listed policy to clear scoped config, got %d", changed)
	}
	cfg = manager.EffectiveStateMachineConfig("user-service")
	if cfg.DegradedThreshold != 9 || cfg.EjectionDuration != 30*time.Second {
		t.Fatalf("expected scoped config removal to restore base config, got %+v", cfg)
	}
}

type staticPolicyListStore struct {
	snapshots []*aegisv1.PolicySnapshot
}

func (s staticPolicyListStore) List() []*aegisv1.PolicySnapshot {
	return s.snapshots
}

func TestRunPolicyHotApplyLoopReloadsChangedFilePolicy(t *testing.T) {
	path := t.TempDir() + "/policy.yaml"
	modTime := time.Now()
	writePolicyHotApplyTestFile(t, path, modTime, `
services:
  user-service:
    outlier_detection:
      degraded_threshold: 0.5
      ejection_duration_seconds: 4
`)
	store, err := policy.NewFileStore(path)
	if err != nil {
		t.Fatalf("new policy file store: %v", err)
	}
	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		StateMachine: fault.StateMachineConfig{
			DegradedThreshold:     5,
			EjectThreshold:        6,
			ConsecutiveWindows:    7,
			EjectionDuration:      8 * time.Second,
			RecoveryThreshold:     2,
			ProbeSuccessThreshold: 0.8,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunPolicyHotApplyLoop(ctx, store, manager, 10*time.Millisecond, nil)

	waitForStateMachineConfig(t, manager, "user-service", func(cfg fault.StateMachineConfig) bool {
		return cfg.DegradedThreshold == 0.5 && cfg.EjectThreshold == 6 && cfg.EjectionDuration == 4*time.Second
	})

	modTime = modTime.Add(time.Second)
	writePolicyHotApplyTestFile(t, path, modTime, `
services:
  user-service:
    outlier_detection:
      degraded_threshold: 0.8
      ejection_duration_seconds: 9
`)
	waitForStateMachineConfig(t, manager, "user-service", func(cfg fault.StateMachineConfig) bool {
		return cfg.DegradedThreshold == 0.8 && cfg.EjectionDuration == 9*time.Second
	})

	modTime = modTime.Add(time.Second)
	writePolicyHotApplyTestFile(t, path, modTime, "services: {}\n")
	waitForStateMachineConfig(t, manager, "user-service", func(cfg fault.StateMachineConfig) bool {
		return cfg.DegradedThreshold == 5 && cfg.EjectionDuration == 8*time.Second
	})
}

func writePolicyHotApplyTestFile(t *testing.T, path string, modTime time.Time, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set policy file modtime: %v", err)
	}
}

func waitForStateMachineConfig(t *testing.T, manager *fault.HealthManager, service string, ok func(fault.StateMachineConfig) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		cfg := manager.EffectiveStateMachineConfig(service)
		if ok(cfg) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for state-machine config, last=%+v", cfg)
		case <-ticker.C:
		}
	}
}

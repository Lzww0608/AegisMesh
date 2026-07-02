package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
)

const defaultPolicyHotApplyInterval = 3 * time.Second

type PolicySnapshotLister interface {
	List() []*aegisv1.PolicySnapshot
}

type StateMachineConfigApplier interface {
	ReplaceServiceStateMachineConfigs(map[string]fault.StateMachineConfig) int
}

func OutlierDetectionToStateMachineConfig(policy *aegisv1.OutlierDetectionPolicy) fault.StateMachineConfig {
	if policy == nil {
		return fault.StateMachineConfig{}
	}
	cfg := fault.StateMachineConfig{
		DegradedThreshold:     policy.GetDegradedThreshold(),
		EjectThreshold:        policy.GetEjectThreshold(),
		ConsecutiveWindows:    int(policy.GetConsecutiveWindows()),
		RecoveryThreshold:     policy.GetRecoveryThreshold(),
		ProbeSuccessThreshold: policy.GetProbeSuccessThreshold(),
	}
	if seconds := policy.GetEjectionDurationSeconds(); seconds > 0 {
		cfg.EjectionDuration = time.Duration(seconds) * time.Second
	}
	return cfg
}

func ApplyOutlierDetectionPolicies(store PolicySnapshotLister, applier StateMachineConfigApplier) int {
	if store == nil || applier == nil {
		return 0
	}

	configs := make(map[string]fault.StateMachineConfig)
	for _, snapshot := range store.List() {
		if snapshot == nil || snapshot.Service == "" {
			continue
		}
		configs[snapshot.Service] = OutlierDetectionToStateMachineConfig(snapshot.OutlierDetection)
	}
	return applier.ReplaceServiceStateMachineConfigs(configs)
}

func RunPolicyHotApplyLoop(ctx context.Context, store PolicySnapshotLister, applier StateMachineConfigApplier, interval time.Duration, logf func(string, ...any)) {
	if interval <= 0 {
		interval = defaultPolicyHotApplyInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if reloader, ok := store.(policyReloader); ok {
				if err := reloader.ReloadIfChanged(); err != nil {
					if logf != nil {
						logf("reload policy for hot apply: %v", err)
					}
					continue
				}
			}
			changed := ApplyOutlierDetectionPolicies(store, applier)
			if changed > 0 && logf != nil {
				logf("applied outlier policy to %d service(s)", changed)
			}
		}
	}
}

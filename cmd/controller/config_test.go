package main

import (
	"testing"
	"time"
)

func TestBuildStateMachineConfigPreservesExperimentThresholds(t *testing.T) {
	cfg := buildStateMachineConfig(0.05, 0.09, 2, 5*time.Second, 0.03, 0.90)

	if cfg.DegradedThreshold != 0.05 {
		t.Fatalf("expected degraded threshold 0.05, got %v", cfg.DegradedThreshold)
	}
	if cfg.EjectThreshold != 0.09 {
		t.Fatalf("expected eject threshold 0.09, got %v", cfg.EjectThreshold)
	}
	if cfg.ConsecutiveWindows != 2 {
		t.Fatalf("expected two consecutive windows, got %d", cfg.ConsecutiveWindows)
	}
	if cfg.EjectionDuration != 5*time.Second {
		t.Fatalf("expected 5s ejection duration, got %s", cfg.EjectionDuration)
	}
	if cfg.RecoveryThreshold != 0.03 {
		t.Fatalf("expected recovery threshold 0.03, got %v", cfg.RecoveryThreshold)
	}
	if cfg.ProbeSuccessThreshold != 0.90 {
		t.Fatalf("expected probe threshold 0.90, got %v", cfg.ProbeSuccessThreshold)
	}
}

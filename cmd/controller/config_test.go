package main

import (
	"flag"
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

func TestBuildHealthManagerConfigPreservesLatencySLO(t *testing.T) {
	cfg := buildHealthManagerConfig(0.05, 0.09, 2, 5*time.Second, 0.03, 0.90, 250*time.Millisecond)

	if cfg.LatencySLO != 250*time.Millisecond {
		t.Fatalf("expected latency SLO 250ms, got %s", cfg.LatencySLO)
	}
	if cfg.StateMachine.DegradedThreshold != 0.05 || cfg.StateMachine.EjectThreshold != 0.09 {
		t.Fatalf("expected state-machine thresholds to be preserved, got %+v", cfg.StateMachine)
	}
}

func TestBuildRegistryFromFlagsSelectsFileBackend(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "file", "--registry-file", t.TempDir() + "/registry.json"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	store, err := buildRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if _, ok := store.(interface{ PersistencePath() string }); !ok {
		t.Fatalf("expected persistent registry backend, got %T", store)
	}
}

func TestRegisterPolicyFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerPolicyFlags(fs)
	if err := fs.Parse([]string{"--policy-file", "policy.yaml", "--policy-reload-interval", "2s"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if *cfg.file != "policy.yaml" {
		t.Fatalf("expected policy file flag, got %q", *cfg.file)
	}
	if *cfg.reloadInterval != 2*time.Second {
		t.Fatalf("expected 2s reload interval, got %s", *cfg.reloadInterval)
	}
}

func TestBuildRegistryFromFlagsSelectsFileV2Backend(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "file-v2", "--registry-file", t.TempDir() + "/registry.json", "--registry-file-v2-sync", "always", "--registry-file-v2-flush-records", "1", "--registry-file-v2-flush-bytes", "256", "--registry-file-v2-flush-interval", "1ms", "--registry-file-v2-compact-bytes", "1024"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	store, err := buildRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	persistent, ok := store.(interface {
		PersistencePath() string
		WALPath() string
		Close() error
	})
	if !ok {
		t.Fatalf("expected file-v2 registry backend, got %T", store)
	}
	if persistent.WALPath() == "" {
		t.Fatalf("expected file-v2 registry to expose WAL path")
	}
	if err := persistent.Close(); err != nil {
		t.Fatalf("close file-v2 registry: %v", err)
	}
}

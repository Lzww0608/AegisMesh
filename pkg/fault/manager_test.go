package fault

import (
	"testing"
	"time"
)

// TestHealthManagerEjectsSustainedSlowEndpoint locks the health manager ejects sustained slow endpoint contract so future changes do not regress it.
func TestHealthManagerEjectsSustainedSlowEndpoint(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{
		Now: func() time.Time { return now },
		StateMachine: StateMachineConfig{
			DegradedThreshold:  1.5,
			EjectThreshold:     2.5,
			ConsecutiveWindows: 3,
			EjectionDuration:   30 * time.Second,
			RecoveryThreshold:  1.0,
		},
	})

	for i := 0; i < 3; i++ {
		manager.Update([]EndpointSample{
			{Service: "user-service", InstanceID: "user-a", Address: "127.0.0.1:7001", RequestCount: 100, LatencyP95: 100 * time.Millisecond, Capacity: 100},
			{Service: "user-service", InstanceID: "user-b", Address: "127.0.0.1:7002", RequestCount: 100, LatencyP95: 110 * time.Millisecond, Capacity: 100},
			{Service: "user-service", InstanceID: "user-c", Address: "127.0.0.1:7003", RequestCount: 100, ErrorCount: 30, Inflight: 95, LatencyP95: 600 * time.Millisecond, TCPRetransmit: 10, Capacity: 100},
		})
		now = now.Add(time.Second)
	}

	health, ok := manager.Get("user-service", "user-c")
	if !ok {
		t.Fatalf("expected health entry for slow endpoint")
	}
	if health.State != StateEjected {
		t.Fatalf("expected sustained slow endpoint to be ejected, got %+v", health)
	}

	healthy, ok := manager.Get("user-service", "user-a")
	if !ok {
		t.Fatalf("expected health entry for healthy endpoint")
	}
	if healthy.State != StateHealthy {
		t.Fatalf("expected healthy endpoint to remain healthy, got %+v", healthy)
	}
}

// TestHealthManagerMovesEjectedEndpointToProbingAfterTTL locks the health manager moves ejected endpoint to probing after ttl contract so future changes do not regress it.
func TestHealthManagerMovesEjectedEndpointToProbingAfterTTL(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{
		Now: func() time.Time { return now },
		StateMachine: StateMachineConfig{
			DegradedThreshold:  1.5,
			EjectThreshold:     2.5,
			ConsecutiveWindows: 1,
			EjectionDuration:   30 * time.Second,
			RecoveryThreshold:  1.0,
		},
	})

	manager.Update([]EndpointSample{
		{Service: "user-service", InstanceID: "user-a", Address: "127.0.0.1:7001", RequestCount: 100, LatencyP95: 100 * time.Millisecond, Capacity: 100},
		{Service: "user-service", InstanceID: "user-b", Address: "127.0.0.1:7002", RequestCount: 100, LatencyP95: 600 * time.Millisecond, Capacity: 100},
		{Service: "user-service", InstanceID: "user-c", Address: "127.0.0.1:7003", RequestCount: 100, LatencyP95: 110 * time.Millisecond, Capacity: 100},
	})
	health, _ := manager.Get("user-service", "user-b")
	if health.State != StateEjected {
		t.Fatalf("expected initial ejection, got %+v", health)
	}

	now = now.Add(31 * time.Second)
	manager.Tick()
	health, _ = manager.Get("user-service", "user-b")
	if health.State != StateProbing {
		t.Fatalf("expected probing after ejection TTL, got %+v", health)
	}
}

// TestHealthManagerServiceStateMachineConfigInheritsBaseAndResetsCounters locks the health manager service state machine config inherits base and resets counters contract so future changes do not regress it.
func TestHealthManagerServiceStateMachineConfigInheritsBaseAndResetsCounters(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{
		Now:        func() time.Time { return now },
		Weights:    ScoreWeights{LatencyWeight: 1},
		LatencySLO: 100 * time.Millisecond,
		StateMachine: StateMachineConfig{
			DegradedThreshold:     10,
			EjectThreshold:        20,
			ConsecutiveWindows:    2,
			EjectionDuration:      30 * time.Second,
			RecoveryThreshold:     5,
			ProbeSuccessThreshold: 0.8,
		},
	})

	if !manager.SetServiceStateMachineConfig("user-service", StateMachineConfig{DegradedThreshold: 1}) {
		t.Fatalf("expected initial service config to change effective config")
	}
	cfg := manager.EffectiveStateMachineConfig("user-service")
	if cfg.DegradedThreshold != 1 || cfg.EjectThreshold != 20 || cfg.ConsecutiveWindows != 2 || cfg.ProbeSuccessThreshold != 0.8 {
		t.Fatalf("expected service config to inherit base zero values, got %+v", cfg)
	}

	manager.Update([]EndpointSample{{
		Service:      "user-service",
		InstanceID:   "user-a",
		Address:      "127.0.0.1:7001",
		RequestCount: 100,
		LatencyP95:   2500 * time.Millisecond,
		Capacity:     100,
	}})
	health, ok := manager.Get("user-service", "user-a")
	if !ok {
		t.Fatalf("expected health entry")
	}
	if health.State != StateHealthy || health.ConsecutiveSlowWindows != 1 || health.ConsecutiveEjectWindows != 1 {
		t.Fatalf("expected first slow window to accumulate counters without transition, got %+v", health)
	}

	changed := manager.ReplaceServiceStateMachineConfigs(map[string]StateMachineConfig{
		"user-service": {DegradedThreshold: 1, ConsecutiveWindows: 1},
	})
	if changed != 1 {
		t.Fatalf("expected one changed service config, got %d", changed)
	}
	health, _ = manager.Get("user-service", "user-a")
	if health.State != StateHealthy || health.ConsecutiveSlowWindows != 0 || health.ConsecutiveEjectWindows != 0 {
		t.Fatalf("expected state preserved and counters reset on config change, got %+v", health)
	}

	now = now.Add(time.Second)
	manager.Update([]EndpointSample{{
		Service:      "user-service",
		InstanceID:   "user-a",
		Address:      "127.0.0.1:7001",
		RequestCount: 100,
		LatencyP95:   2500 * time.Millisecond,
		Capacity:     100,
	}})
	health, _ = manager.Get("user-service", "user-a")
	if health.State != StateEjected {
		t.Fatalf("expected inherited eject threshold with overridden window count to eject, got %+v", health)
	}

	changed = manager.ReplaceServiceStateMachineConfigs(nil)
	if changed != 1 {
		t.Fatalf("expected removing stale service config to change effective config, got %d", changed)
	}
	cfg = manager.EffectiveStateMachineConfig("user-service")
	if cfg.DegradedThreshold != 10 || cfg.ConsecutiveWindows != 2 {
		t.Fatalf("expected service config removal to fall back to base, got %+v", cfg)
	}
	health, _ = manager.Get("user-service", "user-a")
	if health.State != StateEjected {
		t.Fatalf("expected config removal to preserve endpoint state, got %+v", health)
	}
}

// TestHealthManagerPruneMissingRemovesInactiveEndpoints locks the health manager prune missing removes inactive endpoints contract so future changes do not regress it.
func TestHealthManagerPruneMissingRemovesInactiveEndpoints(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{Now: func() time.Time { return now }})
	manager.MergeSnapshot([]EndpointHealth{
		{Service: "user-service", InstanceID: "user-a", Address: "127.0.0.1:7001", State: StateHealthy, UpdatedAt: now},
		{Service: "user-service", InstanceID: "user-b", Address: "127.0.0.1:7002", State: StateEjected, UpdatedAt: now},
	})
	before := manager.HealthVersion("user-service")

	removed := manager.PruneMissing(map[EndpointIdentity]string{
		{Service: "user-service", InstanceID: "user-a"}: "127.0.0.1:7001",
	})
	if removed != 1 {
		t.Fatalf("expected one pruned endpoint, got %d", removed)
	}
	if _, ok := manager.Get("user-service", "user-a"); !ok {
		t.Fatalf("expected retained endpoint to remain")
	}
	if _, ok := manager.Get("user-service", "user-b"); ok {
		t.Fatalf("expected missing endpoint health to be pruned")
	}
	if got := manager.HealthVersion("user-service"); got <= before {
		t.Fatalf("expected prune to bump service health version, before=%d after=%d", before, got)
	}
	if removed := manager.PruneMissing(nil); removed != 0 {
		t.Fatalf("expected nil retain set to be a no-op, got %d", removed)
	}
}

// TestHealthManagerAddressChangeResetsEndpointState locks the health manager address change resets endpoint state contract so future changes do not regress it.
func TestHealthManagerAddressChangeResetsEndpointState(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{Now: func() time.Time { return now }})
	manager.MergeSnapshot([]EndpointHealth{{
		Service:                 "user-service",
		InstanceID:              "user-a",
		Address:                 "127.0.0.1:7001",
		State:                   StateEjected,
		SlowScore:               3,
		ConsecutiveSlowWindows:  2,
		ConsecutiveEjectWindows: 2,
		EjectedAt:               now.Add(-time.Second),
		UpdatedAt:               now.Add(-time.Second),
	}})

	manager.Update([]EndpointSample{{
		Service:      "user-service",
		InstanceID:   "user-a",
		Address:      "127.0.0.1:7101",
		RequestCount: 100,
		LatencyP95:   50 * time.Millisecond,
		Capacity:     100,
	}})
	health, ok := manager.Get("user-service", "user-a")
	if !ok {
		t.Fatalf("expected health entry")
	}
	if health.Address != "127.0.0.1:7101" || health.State != StateHealthy || health.ConsecutiveSlowWindows != 0 || health.ConsecutiveEjectWindows != 0 || !health.EjectedAt.IsZero() {
		t.Fatalf("expected address change to reset old endpoint state, got %+v", health)
	}
}

// TestHealthManagerPruneMissingRemovesAddressMismatches locks the health manager prune missing removes address mismatches contract so future changes do not regress it.
func TestHealthManagerPruneMissingRemovesAddressMismatches(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{Now: func() time.Time { return now }})
	manager.MergeSnapshot([]EndpointHealth{{
		Service:    "user-service",
		InstanceID: "user-a",
		Address:    "127.0.0.1:7001",
		State:      StateEjected,
		UpdatedAt:  now,
	}})

	removed := manager.PruneMissing(map[EndpointIdentity]string{
		{Service: "user-service", InstanceID: "user-a"}: "127.0.0.1:7101",
	})
	if removed != 1 {
		t.Fatalf("expected address-mismatched endpoint to be pruned, got %d", removed)
	}
	if _, ok := manager.Get("user-service", "user-a"); ok {
		t.Fatalf("expected address-mismatched health to be removed")
	}
}

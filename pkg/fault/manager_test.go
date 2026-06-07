package fault

import (
	"testing"
	"time"
)

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

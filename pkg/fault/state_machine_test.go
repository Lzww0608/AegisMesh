package fault

import (
	"testing"
	"time"
)

func TestStateMachineRequiresConsecutiveWindowsBeforeDegrading(t *testing.T) {
	machine := NewStateMachine(StateMachineConfig{
		DegradedThreshold:  1.5,
		EjectThreshold:     2.5,
		ConsecutiveWindows: 3,
		EjectionDuration:   30 * time.Second,
		RecoveryThreshold:  1.0,
	})
	health := NewEndpointHealth("user-service", "user-a", "127.0.0.1:7001")
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 2; i++ {
		machine.Apply(&health, StateInput{Now: now.Add(time.Duration(i) * time.Second), SlowScore: 1.8, SuccessRate: 1})
		if health.State != StateHealthy {
			t.Fatalf("expected healthy before third slow window, got %s", health.State)
		}
	}

	machine.Apply(&health, StateInput{Now: now.Add(2 * time.Second), SlowScore: 1.8, SuccessRate: 1})
	if health.State != StateDegraded {
		t.Fatalf("expected degraded after three slow windows, got %s", health.State)
	}
}

func TestStateMachineEjectsThenMovesThroughProbingBeforeHealthy(t *testing.T) {
	machine := NewStateMachine(StateMachineConfig{
		DegradedThreshold:  1.5,
		EjectThreshold:     2.5,
		ConsecutiveWindows: 3,
		EjectionDuration:   30 * time.Second,
		RecoveryThreshold:  1.0,
	})
	health := NewEndpointHealth("user-service", "user-c", "127.0.0.1:7003")
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		machine.Apply(&health, StateInput{Now: now.Add(time.Duration(i) * time.Second), SlowScore: 3.2, SuccessRate: 1})
	}
	if health.State != StateEjected {
		t.Fatalf("expected ejected after sustained high score, got %s", health.State)
	}

	machine.Apply(&health, StateInput{Now: now.Add(33 * time.Second), SlowScore: 3.2, SuccessRate: 1})
	if health.State != StateProbing {
		t.Fatalf("expected probing after ejection TTL, got %s", health.State)
	}

	machine.Apply(&health, StateInput{Now: now.Add(34 * time.Second), SlowScore: 0.4, SuccessRate: 0.99})
	if health.State != StateHealthy {
		t.Fatalf("expected healthy after successful probe, got %s", health.State)
	}
}

func TestStateMachineReturnsFailedProbeToEjected(t *testing.T) {
	machine := NewStateMachine(StateMachineConfig{
		DegradedThreshold:  1.5,
		EjectThreshold:     2.5,
		ConsecutiveWindows: 3,
		EjectionDuration:   30 * time.Second,
		RecoveryThreshold:  1.0,
	})
	health := NewEndpointHealth("user-service", "user-c", "127.0.0.1:7003")
	health.State = StateProbing

	machine.Apply(&health, StateInput{
		Now:         time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC),
		SlowScore:   2.0,
		SuccessRate: 0.80,
	})

	if health.State != StateEjected {
		t.Fatalf("expected failed probe to return to ejected, got %s", health.State)
	}
}

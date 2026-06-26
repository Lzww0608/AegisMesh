package fault

import (
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

type EndpointState = status.Code

const (
	StateUnspecified = status.Unspecified
	StateHealthy     = status.Healthy
	StateDegraded    = status.Degraded
	StateEjected     = status.Ejected
	StateProbing     = status.Probing
	StateDead        = status.Dead
)

type StateMachineConfig struct {
	DegradedThreshold     float64
	EjectThreshold        float64
	ConsecutiveWindows    int
	EjectionDuration      time.Duration
	RecoveryThreshold     float64
	ProbeSuccessThreshold float64
}

func DefaultStateMachineConfig() StateMachineConfig {
	return StateMachineConfig{
		DegradedThreshold:     1.5,
		EjectThreshold:        2.5,
		ConsecutiveWindows:    3,
		EjectionDuration:      30 * time.Second,
		RecoveryThreshold:     1.0,
		ProbeSuccessThreshold: 0.95,
	}
}

type StateInput struct {
	Now         time.Time
	SlowScore   float64
	SuccessRate float64
}

type EndpointHealth struct {
	Service                 string
	InstanceID              string
	Address                 string
	State                   EndpointState
	SlowScore               float64
	ConsecutiveSlowWindows  int
	ConsecutiveEjectWindows int
	LastTransitionAt        time.Time
	EjectedAt               time.Time
}

func NewEndpointHealth(service, instanceID, address string) EndpointHealth {
	return EndpointHealth{
		Service:    service,
		InstanceID: instanceID,
		Address:    address,
		State:      StateHealthy,
	}
}

type StateMachine struct {
	cfg StateMachineConfig
}

func NewStateMachine(cfg StateMachineConfig) *StateMachine {
	defaults := DefaultStateMachineConfig()
	if cfg.DegradedThreshold <= 0 {
		cfg.DegradedThreshold = defaults.DegradedThreshold
	}
	if cfg.EjectThreshold <= 0 {
		cfg.EjectThreshold = defaults.EjectThreshold
	}
	if cfg.ConsecutiveWindows <= 0 {
		cfg.ConsecutiveWindows = defaults.ConsecutiveWindows
	}
	if cfg.EjectionDuration <= 0 {
		cfg.EjectionDuration = defaults.EjectionDuration
	}
	if cfg.RecoveryThreshold <= 0 {
		cfg.RecoveryThreshold = defaults.RecoveryThreshold
	}
	if cfg.ProbeSuccessThreshold <= 0 {
		cfg.ProbeSuccessThreshold = defaults.ProbeSuccessThreshold
	}
	return &StateMachine{cfg: cfg}
}

func (m *StateMachine) Apply(health *EndpointHealth, input StateInput) {
	if health == nil {
		return
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	if health.State == status.Unspecified {
		health.State = StateHealthy
	}
	health.SlowScore = input.SlowScore

	switch health.State {
	case StateEjected:
		if !health.EjectedAt.IsZero() && now.Sub(health.EjectedAt) >= m.cfg.EjectionDuration {
			m.transition(health, StateProbing, now)
		}
		return
	case StateProbing:
		if input.SuccessRate < m.cfg.ProbeSuccessThreshold || input.SlowScore >= m.cfg.DegradedThreshold {
			m.transition(health, StateEjected, now)
			return
		}
		if input.SlowScore <= m.cfg.RecoveryThreshold {
			m.transition(health, StateHealthy, now)
		}
		return
	}

	if input.SlowScore >= m.cfg.EjectThreshold {
		health.ConsecutiveEjectWindows++
		health.ConsecutiveSlowWindows++
	} else {
		health.ConsecutiveEjectWindows = 0
		if input.SlowScore >= m.cfg.DegradedThreshold {
			health.ConsecutiveSlowWindows++
		} else {
			health.ConsecutiveSlowWindows = 0
		}
	}

	if health.ConsecutiveEjectWindows >= m.cfg.ConsecutiveWindows {
		m.transition(health, StateEjected, now)
		return
	}
	if health.ConsecutiveSlowWindows >= m.cfg.ConsecutiveWindows {
		m.transition(health, StateDegraded, now)
		return
	}
	if health.State == StateDegraded && input.SlowScore <= m.cfg.RecoveryThreshold {
		m.transition(health, StateHealthy, now)
	}
}

func (m *StateMachine) transition(health *EndpointHealth, next EndpointState, now time.Time) {
	if health.State == next {
		return
	}
	health.State = next
	health.LastTransitionAt = now
	health.ConsecutiveSlowWindows = 0
	health.ConsecutiveEjectWindows = 0
	if next == StateEjected {
		health.EjectedAt = now
	}
	if next == StateHealthy {
		health.EjectedAt = time.Time{}
	}
}

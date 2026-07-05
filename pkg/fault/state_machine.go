package fault

import (
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

// EndpointState aliases status codes so fault transitions and routing health share one vocabulary.
type EndpointState = status.Code

const (
	StateUnspecified = status.Unspecified
	StateHealthy     = status.Healthy
	StateDegraded    = status.Degraded
	StateEjected     = status.Ejected
	StateProbing     = status.Probing
	StateDead        = status.Dead
)

// StateMachineConfig defines the thresholds and windows that gate degrade, eject, probe, and recovery transitions.
type StateMachineConfig struct {
	DegradedThreshold     float64
	EjectThreshold        float64
	ConsecutiveWindows    int
	EjectionDuration      time.Duration
	RecoveryThreshold     float64
	ProbeSuccessThreshold float64
}

// DefaultStateMachineConfig returns conservative transition thresholds shared by services without custom policy.
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

// resolveStateMachineConfig refreshes resolver state from the controller.
func resolveStateMachineConfig(base, override StateMachineConfig) StateMachineConfig {
	base = defaultStateMachineConfig(base)
	if override.DegradedThreshold > 0 {
		base.DegradedThreshold = override.DegradedThreshold
	}
	if override.EjectThreshold > 0 {
		base.EjectThreshold = override.EjectThreshold
	}
	if override.ConsecutiveWindows > 0 {
		base.ConsecutiveWindows = override.ConsecutiveWindows
	}
	if override.EjectionDuration > 0 {
		base.EjectionDuration = override.EjectionDuration
	}
	if override.RecoveryThreshold > 0 {
		base.RecoveryThreshold = override.RecoveryThreshold
	}
	if override.ProbeSuccessThreshold > 0 {
		base.ProbeSuccessThreshold = override.ProbeSuccessThreshold
	}
	return base
}

// defaultStateMachineConfig keeps default state machine config rules consistent for fault-state scoring and recovery.
func defaultStateMachineConfig(cfg StateMachineConfig) StateMachineConfig {
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
	return cfg
}

// StateInput carries state input state for fault-state scoring and recovery.
type StateInput struct {
	Now         time.Time
	SlowScore   float64
	SuccessRate float64
}

// EndpointHealth carries endpoint health state for fault-state scoring and recovery.
type EndpointHealth struct {
	Service                 string
	InstanceID              string
	Address                 string
	RegistrationEpoch       string
	State                   EndpointState
	SlowScore               float64
	ConsecutiveSlowWindows  int
	ConsecutiveEjectWindows int
	LastTransitionAt        time.Time
	EjectedAt               time.Time
	UpdatedAt               time.Time
}

// NewEndpointHealth initializes endpoint health with package defaults for this package's call path.
func NewEndpointHealth(service, instanceID, address string, registrationEpoch ...string) EndpointHealth {
	epoch := ""
	if len(registrationEpoch) > 0 {
		epoch = registrationEpoch[0]
	}
	return EndpointHealth{
		Service:           service,
		InstanceID:        instanceID,
		Address:           address,
		RegistrationEpoch: epoch,
		State:             StateHealthy,
	}
}

// StateMachine carries state machine state for fault-state scoring and recovery.
type StateMachine struct {
	cfg StateMachineConfig
}

// NewStateMachine initializes state machine with package defaults for this package's call path.
func NewStateMachine(cfg StateMachineConfig) *StateMachine {
	return &StateMachine{cfg: defaultStateMachineConfig(cfg)}
}

// Apply applies apply to the mutable target while preserving transition rules.
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

// transition applies a state change and resets counters so future windows start from the new state.
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

package fault

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

// HealthManagerConfig wires score weights, SLOs, and per-service state-machine overrides into health evaluation.
type HealthManagerConfig struct {
	Weights              ScoreWeights
	LatencySLO           time.Duration
	StateMachine         StateMachineConfig
	ServiceStateMachines map[string]StateMachineConfig
	Now                  func() time.Time
}

// EndpointIdentity carries endpoint identity state for fault-state scoring and recovery.
type EndpointIdentity struct {
	Service           string
	InstanceID        string
	RegistrationEpoch string
}

// HealthManager scores endpoint telemetry and publishes versioned health state.
type HealthManager struct {
	mu                   sync.RWMutex
	now                  func() time.Time
	calculator           *ScoreCalculator
	baseStateMachine     StateMachineConfig
	serviceStateMachines map[string]StateMachineConfig
	health               map[string]EndpointHealth
	revision             int64
	serviceRevisions     map[string]int64
	notify               chan struct{}
}

// NewHealthManager initializes health manager with package defaults for this package's call path.
func NewHealthManager(cfg HealthManagerConfig) *HealthManager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Weights == (ScoreWeights{}) {
		cfg.Weights = DefaultScoreWeights()
	}
	return &HealthManager{
		now:                  cfg.Now,
		calculator:           NewScoreCalculatorWithConfig(ScoreCalculatorConfig{Weights: cfg.Weights, LatencySLO: cfg.LatencySLO}),
		baseStateMachine:     defaultStateMachineConfig(cfg.StateMachine),
		serviceStateMachines: cloneStateMachineConfigs(cfg.ServiceStateMachines),
		health:               make(map[string]EndpointHealth),
		serviceRevisions:     make(map[string]int64),
		notify:               make(chan struct{}),
	}
}

// SetServiceStateMachineConfig hot-applies one service override and resets affected counters.
func (m *HealthManager) SetServiceStateMachineConfig(service string, cfg StateMachineConfig) bool {
	if service == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	before := m.stateMachineConfigLocked(service)
	if m.serviceStateMachines == nil {
		m.serviceStateMachines = make(map[string]StateMachineConfig)
	}
	m.serviceStateMachines[service] = cfg
	if before == m.stateMachineConfigLocked(service) {
		return false
	}
	m.resetStateMachineCountersLocked(service)
	m.bumpIfChangedLocked(map[string]struct{}{service: {}})
	return true
}

// ReplaceServiceStateMachineConfigs replaces all service overrides and reports changed services.
func (m *HealthManager) ReplaceServiceStateMachineConfigs(configs map[string]StateMachineConfig) int {
	configs = cloneStateMachineConfigs(configs)

	m.mu.Lock()
	defer m.mu.Unlock()

	changedServices := make(map[string]struct{})
	for service, oldConfig := range m.serviceStateMachines {
		oldEffective := resolveStateMachineConfig(m.baseStateMachine, oldConfig)
		newEffective := resolveStateMachineConfig(m.baseStateMachine, configs[service])
		if oldEffective != newEffective {
			changedServices[service] = struct{}{}
		}
	}
	for service, newConfig := range configs {
		if _, existed := m.serviceStateMachines[service]; existed {
			continue
		}
		oldEffective := resolveStateMachineConfig(m.baseStateMachine, StateMachineConfig{})
		newEffective := resolveStateMachineConfig(m.baseStateMachine, newConfig)
		if oldEffective != newEffective {
			changedServices[service] = struct{}{}
		}
	}

	m.serviceStateMachines = configs
	for service := range changedServices {
		m.resetStateMachineCountersLocked(service)
	}
	m.bumpIfChangedLocked(changedServices)
	return len(changedServices)
}

// EffectiveStateMachineConfig returns the resolved per-service state-machine settings.
func (m *HealthManager) EffectiveStateMachineConfig(service string) StateMachineConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateMachineConfigLocked(service)
}

// Update folds telemetry samples into slow scores and endpoint state transitions.
func (m *HealthManager) Update(samples []EndpointSample) []EndpointHealth {
	now := m.now()
	scores := m.calculator.Calculate(samples)

	m.mu.Lock()
	defer m.mu.Unlock()

	changedServices := make(map[string]struct{})
	machines := make(map[string]*StateMachine)
	for _, sample := range samples {
		if sample.Service == "" || sample.InstanceID == "" {
			continue
		}
		key := ScoreKey(sample.Service, sample.InstanceID)
		before, existed := m.health[key]
		health := before
		// Address or epoch changes create a new logical endpoint owner; old health counters must not carry across.
		if health.Service == "" || endpointAddressChanged(health, sample.Address) || endpointRegistrationEpochChanged(health, sample.RegistrationEpoch) {
			health = NewEndpointHealth(sample.Service, sample.InstanceID, sample.Address, sample.RegistrationEpoch)
		}
		if sample.Address != "" {
			health.Address = sample.Address
		}
		if sample.RegistrationEpoch != "" {
			health.RegistrationEpoch = sample.RegistrationEpoch
		}

		score := scores[key]
		m.stateMachineForLocked(health.Service, machines).Apply(&health, StateInput{
			Now:         now,
			SlowScore:   score.Score,
			SuccessRate: successRate(sample),
		})
		if !existed || !endpointHealthEqualIgnoringUpdatedAt(before, health) {
			health.UpdatedAt = now
			changedServices[sample.Service] = struct{}{}
		}
		m.health[key] = health
	}
	m.bumpIfChangedLocked(changedServices)

	return m.listLocked("")
}

// Tick advances timers for endpoints that did not report a fresh sample.
func (m *HealthManager) Tick() []EndpointHealth {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	changedServices := make(map[string]struct{})
	machines := make(map[string]*StateMachine)
	for key, health := range m.health {
		before := health
		m.stateMachineForLocked(health.Service, machines).Apply(&health, StateInput{
			Now:         now,
			SlowScore:   health.SlowScore,
			SuccessRate: 1,
		})
		if before != health {
			health.UpdatedAt = now
			changedServices[health.Service] = struct{}{}
		}
		m.health[key] = health
	}
	m.bumpIfChangedLocked(changedServices)
	return m.listLocked("")
}

// Get returns one endpoint health record by service and instance ID.
func (m *HealthManager) Get(service, instanceID string) (EndpointHealth, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	health, ok := m.health[ScoreKey(service, instanceID)]
	return health, ok
}

// HealthState returns only the routing state for registry overlays.
func (m *HealthManager) HealthState(service, instanceID string) (EndpointState, bool) {
	health, ok := m.Get(service, instanceID)
	if !ok {
		return status.Unspecified, false
	}
	return health.State, true
}

// HealthVersion returns the service-specific revision observed by watchers.
func (m *HealthManager) HealthVersion(service string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.healthVersionLocked(service)
}

// WatchHealth streams monotonically increasing health revisions for one service.
func (m *HealthManager) WatchHealth(ctx context.Context, service string, afterVersion int64) (<-chan int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	updates := make(chan int64, 1)
	go func() {
		defer close(updates)

		lastVersion := afterVersion
		for {
			m.mu.RLock()
			version := m.healthVersionLocked(service)
			// Watchers capture the current notify channel under lock; bumpIfChangedLocked closes and replaces it.
			notify := m.notify
			m.mu.RUnlock()

			if version > lastVersion {
				lastVersion = version
				if !sendLatestVersion(ctx, updates, version) {
					return
				}
				continue
			}

			select {
			case <-ctx.Done():
				return
			case <-notify:
			}
		}
	}()
	return updates, nil
}

// List returns a stable, sorted copy of endpoint health records.
func (m *HealthManager) List(service string) []EndpointHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.listLocked(service)
}

// Snapshot returns a stable copy of all endpoint health records.
func (m *HealthManager) Snapshot() []EndpointHealth {
	return m.List("")
}

// MergeSnapshot restores persisted health while preserving newer in-memory revisions.
func (m *HealthManager) MergeSnapshot(snapshot []EndpointHealth) int {
	now := m.now()
	changedServices := make(map[string]struct{})

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, health := range snapshot {
		if health.Service == "" || health.InstanceID == "" {
			continue
		}
		if health.State == StateUnspecified {
			health.State = StateHealthy
		}
		if health.UpdatedAt.IsZero() {
			health.UpdatedAt = now
		}
		key := ScoreKey(health.Service, health.InstanceID)
		current, existed := m.health[key]
		if existed && current.UpdatedAt.After(health.UpdatedAt) {
			continue
		}
		if existed && endpointHealthEqualIgnoringUpdatedAt(current, health) {
			if health.UpdatedAt.After(current.UpdatedAt) {
				current.UpdatedAt = health.UpdatedAt
				m.health[key] = current
			}
			continue
		}
		m.health[key] = health
		changedServices[health.Service] = struct{}{}
	}
	m.bumpIfChangedLocked(changedServices)
	return len(changedServices)
}

// PruneMissing returns prune missing data for HealthManager callers without handing out mutable receiver state.
func (m *HealthManager) PruneMissing(retain map[EndpointIdentity]string) int {
	if retain == nil {
		return 0
	}

	changedServices := make(map[string]struct{})
	removed := 0

	m.mu.Lock()
	defer m.mu.Unlock()

	for key, health := range m.health {
		registeredAddress, ok := retainedEndpointAddress(retain, health)
		if ok && endpointAddressMatches(health.Address, registeredAddress) {
			continue
		}
		delete(m.health, key)
		changedServices[health.Service] = struct{}{}
		removed++
	}
	m.bumpIfChangedLocked(changedServices)
	return removed
}

// listLocked returns a point-in-time list of list locked visible to the caller.
func (m *HealthManager) listLocked(service string) []EndpointHealth {
	out := make([]EndpointHealth, 0, len(m.health))
	for _, health := range m.health {
		if service != "" && health.Service != service {
			continue
		}
		out = append(out, health)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

// stateMachineForLocked returns state machine for locked data for HealthManager callers without handing out mutable receiver state.
func (m *HealthManager) stateMachineForLocked(service string, cache map[string]*StateMachine) *StateMachine {
	if machine := cache[service]; machine != nil {
		return machine
	}
	machine := NewStateMachine(m.stateMachineConfigLocked(service))
	cache[service] = machine
	return machine
}

// stateMachineConfigLocked returns state machine config locked data for HealthManager callers without handing out mutable receiver state.
func (m *HealthManager) stateMachineConfigLocked(service string) StateMachineConfig {
	return resolveStateMachineConfig(m.baseStateMachine, m.serviceStateMachines[service])
}

// resetStateMachineCountersLocked clears slow/eject windows for all endpoints of a service after policy changes.
func (m *HealthManager) resetStateMachineCountersLocked(service string) {
	now := m.now()
	for key, health := range m.health {
		if health.Service != service {
			continue
		}
		if health.ConsecutiveSlowWindows == 0 && health.ConsecutiveEjectWindows == 0 {
			continue
		}
		health.ConsecutiveSlowWindows = 0
		health.ConsecutiveEjectWindows = 0
		health.UpdatedAt = now
		m.health[key] = health
	}
}

// endpointHealthEqualIgnoringUpdatedAt compares snapshots while ignoring clock-only churn.
func endpointHealthEqualIgnoringUpdatedAt(a, b EndpointHealth) bool {
	a.UpdatedAt = time.Time{}
	b.UpdatedAt = time.Time{}
	return a == b
}

// endpointAddressChanged treats newly reported registry addresses as identity changes.
func endpointAddressChanged(health EndpointHealth, address string) bool {
	return address != "" && health.Address != "" && health.Address != address
}

// endpointRegistrationEpochChanged fences health state when a registry lease is replaced.
func endpointRegistrationEpochChanged(health EndpointHealth, registrationEpoch string) bool {
	return registrationEpoch != "" && health.RegistrationEpoch != "" && health.RegistrationEpoch != registrationEpoch
}

// retainedEndpointAddress keeps existing health only for endpoints retained by the registry snapshot.
func retainedEndpointAddress(retain map[EndpointIdentity]string, health EndpointHealth) (string, bool) {
	identity := EndpointIdentity{Service: health.Service, InstanceID: health.InstanceID, RegistrationEpoch: health.RegistrationEpoch}
	if address, ok := retain[identity]; ok {
		return address, true
	}
	legacy := EndpointIdentity{Service: health.Service, InstanceID: health.InstanceID}
	if address, ok := retain[legacy]; ok {
		return address, true
	}
	if health.RegistrationEpoch == "" {
		for candidate, address := range retain {
			if candidate.Service == health.Service && candidate.InstanceID == health.InstanceID {
				return address, true
			}
		}
	}
	return "", false
}

// endpointAddressMatches allows legacy empty addresses but rejects conflicting modern ones.
func endpointAddressMatches(healthAddress, registeredAddress string) bool {
	return healthAddress == "" || registeredAddress == "" || healthAddress == registeredAddress
}

// cloneStateMachineConfigs returns an isolated copy of clone state machine configs input so callers cannot mutate shared state.
func cloneStateMachineConfigs(src map[string]StateMachineConfig) map[string]StateMachineConfig {
	out := make(map[string]StateMachineConfig, len(src))
	for service, cfg := range src {
		if service == "" {
			continue
		}
		out[service] = cfg
	}
	return out
}

// bumpIfChangedLocked advances revisions and coalesces watcher notifications.
func (m *HealthManager) bumpIfChangedLocked(changedServices map[string]struct{}) {
	if len(changedServices) == 0 {
		return
	}
	m.revision++
	for service := range changedServices {
		m.serviceRevisions[service] = m.revision
	}
	close(m.notify)
	m.notify = make(chan struct{})
}

// healthVersionLocked returns health version locked data for HealthManager callers without handing out mutable receiver state.
func (m *HealthManager) healthVersionLocked(service string) int64 {
	if service == "" {
		return m.revision
	}
	return m.serviceRevisions[service]
}

// successRate reports zero when no requests exist so probes do not create NaN metrics.
func successRate(sample EndpointSample) float64 {
	if sample.RequestCount <= 0 {
		return 1
	}
	successes := sample.RequestCount - sample.ErrorCount
	if successes < 0 {
		successes = 0
	}
	return float64(successes) / float64(sample.RequestCount)
}

// sendLatestVersion coalesces watch notifications by replacing stale pending versions.
func sendLatestVersion(ctx context.Context, updates chan int64, version int64) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	select {
	case updates <- version:
		return true
	case <-ctx.Done():
		return false
	default:
		select {
		case <-updates:
		case <-ctx.Done():
			return false
		}
		select {
		case updates <- version:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

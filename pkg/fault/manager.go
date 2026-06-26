package fault

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

type HealthManagerConfig struct {
	Weights      ScoreWeights
	LatencySLO   time.Duration
	StateMachine StateMachineConfig
	Now          func() time.Time
}

type HealthManager struct {
	mu               sync.RWMutex
	now              func() time.Time
	calculator       *ScoreCalculator
	machine          *StateMachine
	health           map[string]EndpointHealth
	revision         int64
	serviceRevisions map[string]int64
	notify           chan struct{}
}

func NewHealthManager(cfg HealthManagerConfig) *HealthManager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Weights == (ScoreWeights{}) {
		cfg.Weights = DefaultScoreWeights()
	}
	return &HealthManager{
		now:              cfg.Now,
		calculator:       NewScoreCalculatorWithConfig(ScoreCalculatorConfig{Weights: cfg.Weights, LatencySLO: cfg.LatencySLO}),
		machine:          NewStateMachine(cfg.StateMachine),
		health:           make(map[string]EndpointHealth),
		serviceRevisions: make(map[string]int64),
		notify:           make(chan struct{}),
	}
}

func (m *HealthManager) Update(samples []EndpointSample) []EndpointHealth {
	now := m.now()
	scores := m.calculator.Calculate(samples)

	m.mu.Lock()
	defer m.mu.Unlock()

	changedServices := make(map[string]struct{})
	for _, sample := range samples {
		if sample.Service == "" || sample.InstanceID == "" {
			continue
		}
		key := ScoreKey(sample.Service, sample.InstanceID)
		before, existed := m.health[key]
		health := before
		if health.Service == "" {
			health = NewEndpointHealth(sample.Service, sample.InstanceID, sample.Address)
		}
		if sample.Address != "" {
			health.Address = sample.Address
		}

		score := scores[key]
		m.machine.Apply(&health, StateInput{
			Now:         now,
			SlowScore:   score.Score,
			SuccessRate: successRate(sample),
		})
		if !existed || before != health {
			changedServices[sample.Service] = struct{}{}
		}
		m.health[key] = health
	}
	m.bumpIfChangedLocked(changedServices)

	return m.listLocked("")
}

func (m *HealthManager) Tick() []EndpointHealth {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	changedServices := make(map[string]struct{})
	for key, health := range m.health {
		before := health
		m.machine.Apply(&health, StateInput{
			Now:         now,
			SlowScore:   health.SlowScore,
			SuccessRate: 1,
		})
		if before != health {
			changedServices[health.Service] = struct{}{}
		}
		m.health[key] = health
	}
	m.bumpIfChangedLocked(changedServices)
	return m.listLocked("")
}

func (m *HealthManager) Get(service, instanceID string) (EndpointHealth, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	health, ok := m.health[ScoreKey(service, instanceID)]
	return health, ok
}

func (m *HealthManager) HealthState(service, instanceID string) (EndpointState, bool) {
	health, ok := m.Get(service, instanceID)
	if !ok {
		return status.Unspecified, false
	}
	return health.State, true
}

func (m *HealthManager) HealthVersion(service string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.healthVersionLocked(service)
}

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

func (m *HealthManager) List(service string) []EndpointHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.listLocked(service)
}

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

func (m *HealthManager) healthVersionLocked(service string) int64 {
	if service == "" {
		return m.revision
	}
	return m.serviceRevisions[service]
}

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

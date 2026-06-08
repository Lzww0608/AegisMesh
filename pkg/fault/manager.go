package fault

import (
	"sort"
	"sync"
	"time"
)

type HealthManagerConfig struct {
	Weights      ScoreWeights
	LatencySLO   time.Duration
	StateMachine StateMachineConfig
	Now          func() time.Time
}

type HealthManager struct {
	mu         sync.RWMutex
	now        func() time.Time
	calculator *ScoreCalculator
	machine    *StateMachine
	health     map[string]EndpointHealth
}

func NewHealthManager(cfg HealthManagerConfig) *HealthManager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Weights == (ScoreWeights{}) {
		cfg.Weights = DefaultScoreWeights()
	}
	return &HealthManager{
		now:        cfg.Now,
		calculator: NewScoreCalculatorWithConfig(ScoreCalculatorConfig{Weights: cfg.Weights, LatencySLO: cfg.LatencySLO}),
		machine:    NewStateMachine(cfg.StateMachine),
		health:     make(map[string]EndpointHealth),
	}
}

func (m *HealthManager) Update(samples []EndpointSample) []EndpointHealth {
	now := m.now()
	scores := m.calculator.Calculate(samples)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sample := range samples {
		if sample.Service == "" || sample.InstanceID == "" {
			continue
		}
		key := ScoreKey(sample.Service, sample.InstanceID)
		health := m.health[key]
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
		m.health[key] = health
	}

	return m.listLocked("")
}

func (m *HealthManager) Tick() []EndpointHealth {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	for key, health := range m.health {
		m.machine.Apply(&health, StateInput{
			Now:         now,
			SlowScore:   health.SlowScore,
			SuccessRate: 1,
		})
		m.health[key] = health
	}
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
		return "", false
	}
	return health.State, true
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

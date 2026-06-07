package routing

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

var ErrNoEndpoint = errors.New("no available endpoint")

type EndpointStatus string

const (
	EndpointHealthy     EndpointStatus = "HEALTHY"
	EndpointDegraded    EndpointStatus = "DEGRADED"
	EndpointEjected     EndpointStatus = "EJECTED"
	EndpointProbing     EndpointStatus = "PROBING"
	EndpointDead        EndpointStatus = "DEAD"
	EndpointUnavailable EndpointStatus = "UNAVAILABLE"
)

type Endpoint struct {
	ID          string
	Address     string
	Status      EndpointStatus
	Inflight    int64
	LatencyEWMA time.Duration
	Weight      float64
	SlowScore   float64
}

type Picker interface {
	Pick(ctx context.Context) (Endpoint, error)
	Update(endpoints []Endpoint)
}

type RoundRobinPicker struct {
	mu        sync.Mutex
	endpoints []Endpoint
	next      int
}

func NewRoundRobinPicker(endpoints []Endpoint) *RoundRobinPicker {
	p := &RoundRobinPicker{}
	p.Update(endpoints)
	return p
}

func (p *RoundRobinPicker) Pick(ctx context.Context) (Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.endpoints) == 0 {
		return Endpoint{}, ErrNoEndpoint
	}

	for attempts := 0; attempts < len(p.endpoints); attempts++ {
		idx := p.next % len(p.endpoints)
		p.next = (p.next + 1) % len(p.endpoints)

		endpoint := p.endpoints[idx]
		if endpoint.Status == "" || endpoint.Status == EndpointHealthy {
			return endpoint, nil
		}
	}

	return Endpoint{}, ErrNoEndpoint
}

func (p *RoundRobinPicker) Update(endpoints []Endpoint) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.endpoints = append(p.endpoints[:0], endpoints...)
	if len(p.endpoints) == 0 {
		p.next = 0
		return
	}
	p.next %= len(p.endpoints)
}

type RandomSource interface {
	Intn(n int) int
}

type AdaptiveP2CConfig struct {
	Random           RandomSource
	LatencyPenalty   float64
	SlowPenalty      float64
	LeastBadFallback bool
}

type AdaptiveP2CPicker struct {
	mu        sync.Mutex
	endpoints []Endpoint
	random    RandomSource
	cfg       AdaptiveP2CConfig
}

func NewAdaptiveP2CPicker(endpoints []Endpoint, cfg AdaptiveP2CConfig) *AdaptiveP2CPicker {
	if cfg.Random == nil {
		cfg.Random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if cfg.LatencyPenalty <= 0 {
		cfg.LatencyPenalty = 1
	}
	if cfg.SlowPenalty <= 0 {
		cfg.SlowPenalty = 1
	}
	p := &AdaptiveP2CPicker{random: cfg.Random, cfg: cfg}
	p.Update(endpoints)
	return p
}

func (p *AdaptiveP2CPicker) Pick(ctx context.Context) (Endpoint, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Endpoint{}, err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	candidates := routableEndpoints(p.endpoints)
	if len(candidates) == 0 {
		return Endpoint{}, ErrNoEndpoint
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	a := p.random.Intn(len(candidates))
	b := p.random.Intn(len(candidates) - 1)
	if b >= a {
		b++
	}

	if endpointCost(candidates[a], p.cfg) <= endpointCost(candidates[b], p.cfg) {
		return candidates[a], nil
	}
	return candidates[b], nil
}

func (p *AdaptiveP2CPicker) Update(endpoints []Endpoint) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.endpoints = append(p.endpoints[:0], endpoints...)
}

func routableEndpoints(endpoints []Endpoint) []Endpoint {
	out := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		switch endpoint.Status {
		case "", EndpointHealthy, EndpointDegraded, EndpointProbing:
			out = append(out, endpoint)
		}
	}
	return out
}

func endpointCost(endpoint Endpoint, cfg AdaptiveP2CConfig) float64 {
	weight := endpoint.Weight
	if weight <= 0 || math.IsNaN(weight) {
		weight = 1
	}
	slowScore := math.Max(0, endpoint.SlowScore)
	effectiveWeight := weight / (1 + slowScore)
	if effectiveWeight <= 0 {
		effectiveWeight = 1
	}
	inflightCost := float64(maxInt64(endpoint.Inflight, 0)) / effectiveWeight
	latencyCost := endpoint.LatencyEWMA.Seconds() * cfg.LatencyPenalty
	slowCost := slowScore * cfg.SlowPenalty
	if endpoint.Status == EndpointDegraded {
		slowCost += 1
	}
	return inflightCost + latencyCost + slowCost
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

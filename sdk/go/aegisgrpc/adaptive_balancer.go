package aegisgrpc

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
	"github.com/aegismesh/aegismesh/pkg/routing"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
)

const adaptiveP2CBalancerName = "aegis_adaptive_p2c"

var (
	registerBalancerOnce sync.Once
	adaptiveStats        sync.Map
	defaultBreaker       = circuitbreaker.NewBreaker(circuitbreaker.Config{MaxInflightPerEndpoint: 128})
)

func registerDefaultBalancer() {
	registerBalancerOnce.Do(func() {
		balancer.Register(base.NewBalancerBuilder(adaptiveP2CBalancerName, adaptivePickerBuilder{}, base.Config{}))
	})
}

type adaptivePickerBuilder struct {
	random routing.RandomSource
}

func (b adaptivePickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	items := make([]adaptivePickerItem, 0, len(info.ReadySCs))
	for sc, scInfo := range info.ReadySCs {
		item := newAdaptivePickerItem(sc, scInfo.Address)
		items = append(items, item)
	}
	random := b.random
	if random == nil {
		random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &adaptivePicker{
		items:   items,
		random:  random,
		breaker: defaultBreaker,
	}
}

type adaptivePicker struct {
	items   []adaptivePickerItem
	random  routing.RandomSource
	breaker *circuitbreaker.Breaker
}

func (p *adaptivePicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	if len(p.items) == 0 {
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}

	endpoints := make([]routing.Endpoint, 0, len(p.items))
	byAddress := make(map[string]adaptivePickerItem, len(p.items))
	for _, item := range p.items {
		endpoint := item.endpoint
		endpoint.Inflight = item.stats.Inflight()
		endpoint.LatencyEWMA = item.stats.LatencyEWMA()
		endpoints = append(endpoints, endpoint)
		byAddress[endpoint.Address] = item
	}

	selected, err := routing.NewAdaptiveP2CPicker(endpoints, routing.AdaptiveP2CConfig{
		Random:           p.random,
		LeastBadFallback: true,
	}).Pick(info.Ctx)
	if err != nil {
		return balancer.PickResult{}, status.Error(codes.Unavailable, err.Error())
	}
	item := byAddress[selected.Address]

	release, err := p.breaker.Acquire(selected.Address)
	if err != nil {
		return balancer.PickResult{}, status.Error(codes.ResourceExhausted, err.Error())
	}
	item.stats.IncrementInflight()
	started := time.Now()

	return balancer.PickResult{
		SubConn: item.subConn,
		Done: func(done balancer.DoneInfo) {
			item.stats.DecrementInflight()
			item.stats.ObserveLatency(time.Since(started))
			release()
		},
	}, nil
}

type adaptivePickerItem struct {
	subConn  balancer.SubConn
	endpoint routing.Endpoint
	stats    *adaptiveEndpointStats
}

func newAdaptivePickerItem(sc balancer.SubConn, address resolver.Address) adaptivePickerItem {
	endpointID := instanceIDFromAttributes(address.Attributes)
	if endpointID == "" {
		endpointID = address.Addr
	}
	statusValue := endpointStatusFromAttributes(address.Attributes)
	if statusValue == "" {
		statusValue = string(routing.EndpointHealthy)
	}
	return adaptivePickerItem{
		subConn: sc,
		endpoint: routing.Endpoint{
			ID:        endpointID,
			Address:   address.Addr,
			Status:    routing.EndpointStatus(statusValue),
			SlowScore: slowScoreFromAttributes(address.Attributes),
			Weight:    1,
		},
		stats: statsForEndpoint(address.Addr),
	}
}

type adaptiveEndpointStats struct {
	inflight atomic.Int64
	mu       sync.Mutex
	ewma     time.Duration
	seen     bool
}

func statsForEndpoint(address string) *adaptiveEndpointStats {
	value, _ := adaptiveStats.LoadOrStore(address, &adaptiveEndpointStats{})
	return value.(*adaptiveEndpointStats)
}

func (s *adaptiveEndpointStats) Inflight() int64 {
	return s.inflight.Load()
}

func (s *adaptiveEndpointStats) IncrementInflight() {
	s.inflight.Add(1)
}

func (s *adaptiveEndpointStats) DecrementInflight() {
	for {
		current := s.inflight.Load()
		if current <= 0 {
			return
		}
		if s.inflight.CompareAndSwap(current, current-1) {
			return
		}
	}
}

func (s *adaptiveEndpointStats) ObserveLatency(sample time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen {
		s.ewma = sample
		s.seen = true
		return
	}
	s.ewma = time.Duration(0.2*float64(sample) + 0.8*float64(s.ewma))
}

func (s *adaptiveEndpointStats) LatencyEWMA() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ewma
}

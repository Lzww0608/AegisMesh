package aegisgrpc

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
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
)

const (
	adaptiveDefaultProbeThreshold       = 2
	adaptiveDefaultMaxInflightPerTarget = int64(128)
	adaptiveRandomIncrement             = uint64(0x9e3779b97f4a7c15)
)

func registerDefaultBalancer() {
	registerBalancerOnce.Do(func() {
		balancer.Register(base.NewBalancerBuilder(adaptiveP2CBalancerName, adaptivePickerBuilder{}, base.Config{}))
	})
}

type adaptivePickerBuilder struct {
	random adaptiveRandomSource
}

func (b adaptivePickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	items := make([]adaptivePickerItem, 0, len(info.ReadySCs))
	normalIndexes := make([]int, 0, len(info.ReadySCs))
	probingIndexes := make([]int, 0, len(info.ReadySCs))
	for sc, scInfo := range info.ReadySCs {
		item, ok := newAdaptivePickerItem(sc, scInfo.Address)
		if !ok {
			continue
		}
		idx := len(items)
		items = append(items, item)
		switch item.status {
		case adaptiveStatusHealthy, adaptiveStatusDegraded:
			normalIndexes = append(normalIndexes, idx)
		case adaptiveStatusProbing:
			probingIndexes = append(probingIndexes, idx)
		}
	}
	random := b.random
	if random == nil {
		random = newAdaptiveAtomicRandomSource(uint64(time.Now().UnixNano()))
	}
	picker := &adaptivePicker{
		items:          items,
		normalIndexes:  normalIndexes,
		probingIndexes: probingIndexes,
		random:         random,
		probeThreshold: adaptiveDefaultProbeThreshold,
	}
	picker.completions.New = newAdaptiveCompletion
	return picker
}

type adaptivePicker struct {
	items          []adaptivePickerItem
	normalIndexes  []int
	probingIndexes []int
	random         adaptiveRandomSource
	probeThreshold int
	completions    sync.Pool
}

func (p *adaptivePicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	if len(p.items) == 0 {
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}
	if info.Ctx != nil {
		if err := info.Ctx.Err(); err != nil {
			return balancer.PickResult{}, status.Error(codes.Unavailable, err.Error())
		}
	}

	item := p.pickItem()
	if item == nil {
		return balancer.PickResult{}, status.Error(codes.Unavailable, "no available endpoint")
	}

	if !item.limiter.TryAcquire() {
		return balancer.PickResult{}, status.Error(codes.ResourceExhausted, circuitbreaker.ErrOpen.Error())
	}
	item.stats.IncrementInflight()
	completion := p.completions.Get().(*adaptiveCompletion)
	completion.picker = p
	completion.item = item
	completion.started = time.Now()

	return balancer.PickResult{
		SubConn: item.subConn,
		Done:    completion.done,
	}, nil
}

type adaptiveCompletion struct {
	picker  *adaptivePicker
	item    *adaptivePickerItem
	started time.Time
	done    func(balancer.DoneInfo)
}

func newAdaptiveCompletion() any {
	completion := &adaptiveCompletion{}
	completion.done = completion.finish
	return completion
}

func (c *adaptiveCompletion) finish(balancer.DoneInfo) {
	picker := c.picker
	item := c.item
	started := c.started
	if picker == nil || item == nil {
		return
	}

	c.picker = nil
	c.item = nil
	c.started = time.Time{}

	item.stats.DecrementInflight()
	item.stats.ObserveLatency(time.Since(started))
	item.limiter.Release()
	picker.completions.Put(c)
}

func (p *adaptivePicker) pickItem() *adaptivePickerItem {
	candidates := p.pickCandidateIndexes()
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &p.items[candidates[0]]
	}

	aPos := p.random.Intn(len(candidates))
	bPos := p.random.Intn(len(candidates) - 1)
	if bPos >= aPos {
		bPos++
	}

	aIdx := candidates[aPos]
	bIdx := candidates[bPos]
	if p.items[aIdx].cost() <= p.items[bIdx].cost() {
		return &p.items[aIdx]
	}
	return &p.items[bIdx]
}

func (p *adaptivePicker) pickCandidateIndexes() []int {
	switch {
	case len(p.normalIndexes) == 0:
		return p.probingIndexes
	case len(p.probingIndexes) == 0:
		return p.normalIndexes
	case p.probeThreshold > 0 && p.random.Intn(100) < p.probeThreshold:
		return p.probingIndexes
	default:
		return p.normalIndexes
	}
}

type adaptivePickerItem struct {
	subConn         balancer.SubConn
	address         string
	status          adaptiveEndpointStatus
	inflightPenalty float64
	latencyPenalty  float64
	staticCost      float64
	stats           *adaptiveEndpointStats
	limiter         *circuitbreaker.EndpointLimiter
}

func newAdaptivePickerItem(sc balancer.SubConn, address resolver.Address) (adaptivePickerItem, bool) {
	statusValue := adaptiveStatusFromString(endpointStatusFromAttributes(address.Attributes))
	if statusValue == adaptiveStatusExcluded {
		return adaptivePickerItem{}, false
	}

	weight := 1.0
	if weight <= 0 || math.IsNaN(weight) {
		weight = 1
	}
	slowScore := math.Max(0, slowScoreFromAttributes(address.Attributes))
	effectiveWeight := weight / (1 + slowScore)
	if effectiveWeight <= 0 {
		effectiveWeight = 1
	}
	staticCost := slowScore
	if statusValue == adaptiveStatusDegraded {
		staticCost++
	}

	return adaptivePickerItem{
		subConn:         sc,
		address:         address.Addr,
		status:          statusValue,
		inflightPenalty: 1 / effectiveWeight,
		latencyPenalty:  1,
		staticCost:      staticCost,
		stats:           statsForEndpoint(address.Addr),
		limiter:         circuitbreaker.NewEndpointLimiter(adaptiveDefaultMaxInflightPerTarget),
	}, true
}

func (i *adaptivePickerItem) cost() float64 {
	return float64(maxInt64(i.stats.Inflight(), 0))*i.inflightPenalty +
		i.stats.LatencySeconds()*i.latencyPenalty +
		i.staticCost
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

func (s *adaptiveEndpointStats) LatencySeconds() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ewma.Seconds()
}

type adaptiveEndpointStatus uint8

const (
	adaptiveStatusExcluded adaptiveEndpointStatus = iota
	adaptiveStatusHealthy
	adaptiveStatusDegraded
	adaptiveStatusProbing
)

func adaptiveStatusFromString(status string) adaptiveEndpointStatus {
	switch status {
	case "", "HEALTHY":
		return adaptiveStatusHealthy
	case "DEGRADED":
		return adaptiveStatusDegraded
	case "PROBING":
		return adaptiveStatusProbing
	default:
		return adaptiveStatusExcluded
	}
}

type adaptiveRandomSource interface {
	Intn(n int) int
}

type adaptiveAtomicRandomSource struct {
	state atomic.Uint64
}

func newAdaptiveAtomicRandomSource(seed uint64) *adaptiveAtomicRandomSource {
	if seed == 0 {
		seed = adaptiveRandomIncrement
	}
	source := &adaptiveAtomicRandomSource{}
	source.state.Store(seed)
	return source
}

func (r *adaptiveAtomicRandomSource) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

func (r *adaptiveAtomicRandomSource) next() uint64 {
	z := r.state.Add(adaptiveRandomIncrement)
	z = (z ^ (z >> 30)) * uint64(0xbf58476d1ce4e5b9)
	z = (z ^ (z >> 27)) * uint64(0x94d049bb133111eb)
	return z ^ (z >> 31)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

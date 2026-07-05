package aegisgrpc

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/align"
	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
)

const adaptiveP2CBalancerName = "aegis_adaptive_p2c"

var (
	// registerBalancerOnce keeps balancer registration idempotent across repeated Dial helpers.
	registerBalancerOnce sync.Once
	adaptiveStats        sync.Map
)

const (
	adaptiveDefaultProbeThreshold       = 2
	adaptiveDefaultMaxInflightPerTarget = int64(128)
	adaptiveRandomIncrement             = uint64(0x9e3779b97f4a7c15)
)

// registerDefaultBalancer installs the process-wide adaptive P2C balancer once.
func registerDefaultBalancer() {
	registerBalancerOnce.Do(func() {
		balancer.Register(base.NewBalancerBuilder(adaptiveP2CBalancerName, adaptivePickerBuilder{}, base.Config{}))
	})
}

// adaptivePickerBuilder carries adaptive picker builder state for resolver, picker, and reporter state.
type adaptivePickerBuilder struct {
	random adaptiveRandomSource
}

// Build precomputes routable endpoint slices for one gRPC picker generation.
func (b adaptivePickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	items := make([]adaptivePickerItem, 0, len(info.ReadySCs))
	normalIndexes := make([]int, 0, len(info.ReadySCs))
	probingIndexes := make([]int, 0, len(info.ReadySCs))
	// Build may allocate because gRPC calls it on resolver updates; Pick must stay
	// focused on precomputed slices and live atomic stats.
	for sc, scInfo := range info.ReadySCs {
		item, ok := newAdaptivePickerItem(sc, scInfo.Address)
		if !ok {
			continue
		}
		idx := len(items)
		items = append(items, item)
		switch {
		case item.status.NormalTraffic():
			normalIndexes = append(normalIndexes, idx)
		case item.status.IsProbing():
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

// adaptivePicker carries adaptive picker state for resolver, picker, and reporter state.
type adaptivePicker struct {
	items          []adaptivePickerItem
	normalIndexes  []int
	probingIndexes []int
	random         adaptiveRandomSource
	probeThreshold int
	completions    sync.Pool
}

// Pick selects the best endpoint candidate for the current RPC.
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

	// Capacity is acquired before returning the SubConn and released by Done.
	// Keeping that symmetry here avoids leaked permits on completed RPCs.
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

// adaptiveCompletion carries adaptive completion state for resolver, picker, and reporter state.
type adaptiveCompletion struct {
	picker  *adaptivePicker
	item    *adaptivePickerItem
	started time.Time
	done    func(balancer.DoneInfo)
}

// newAdaptiveCompletion initializes adaptive completion with package defaults for this package's call path.
func newAdaptiveCompletion() any {
	completion := &adaptiveCompletion{}
	completion.done = completion.finish
	return completion
}

// finish releases one Pick acquisition and returns the completion wrapper to the pool.
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

// pickItem selects the best endpoint candidate for the current RPC.
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

// pickCandidateIndexes separates normal traffic from bounded recovery probes.
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

// adaptivePickerItem carries adaptive picker item state for resolver, picker, and reporter state.
type adaptivePickerItem struct {
	subConn         balancer.SubConn
	address         string
	status          aegisstatus.Code
	inflightPenalty float64
	latencyPenalty  float64
	staticCost      float64
	stats           *adaptiveEndpointStats
	limiter         *circuitbreaker.EndpointLimiter
}

// newAdaptivePickerItem converts one resolver address into hot-path picker state.
func newAdaptivePickerItem(sc balancer.SubConn, address resolver.Address) (adaptivePickerItem, bool) {
	statusValue := aegisstatus.Normalized(endpointStatusFromAttributes(address.Attributes))
	if !statusValue.Routable() {
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
	if statusValue == aegisstatus.Degraded {
		staticCost++
	}
	limiter := circuitbreaker.NewEndpointLimiter(adaptiveDefaultMaxInflightPerTarget)
	// Resolver attributes carry the shared limiter pool so picker rebuilds do not
	// reset per-endpoint in-flight limits on every controller update.
	if limiterPool := limiterPoolFromAttributes(address.Attributes); limiterPool != nil {
		limiter = limiterPool.limiter(address.Addr)
	}

	return adaptivePickerItem{
		subConn:         sc,
		address:         address.Addr,
		status:          statusValue,
		inflightPenalty: 1 / effectiveWeight,
		latencyPenalty:  1,
		staticCost:      staticCost,
		stats:           statsForEndpoint(address.Addr),
		limiter:         limiter,
	}, true
}

// cost combines live pressure, EWMA latency, slow_score, and state penalties.
func (i *adaptivePickerItem) cost() float64 {
	return float64(maxInt64(i.stats.Inflight(), 0))*i.inflightPenalty +
		i.stats.LatencySeconds()*i.latencyPenalty +
		i.staticCost
}

// adaptiveEndpointHot carries adaptive endpoint hot state for resolver, picker, and reporter state.
type adaptiveEndpointHot struct {
	inflight  atomic.Int64
	ewmaNanos atomic.Uint64
	_         align.Pad48
}

// adaptiveEndpointStats carries adaptive endpoint stats state for resolver, picker, and reporter state.
type adaptiveEndpointStats struct {
	hot adaptiveEndpointHot
}

// statsForEndpoint returns the process-local hot counters for an endpoint address.
func statsForEndpoint(address string) *adaptiveEndpointStats {
	value, _ := adaptiveStats.LoadOrStore(address, &adaptiveEndpointStats{})
	return value.(*adaptiveEndpointStats)
}

// Inflight returns inflight data for adaptiveEndpointStats callers without handing out mutable receiver state.
func (s *adaptiveEndpointStats) Inflight() int64 {
	return s.hot.inflight.Load()
}

// IncrementInflight records one in-flight RPC on the picker hot path.
func (s *adaptiveEndpointStats) IncrementInflight() {
	s.hot.inflight.Add(1)
}

// DecrementInflight removes one in-flight RPC without allowing negative pressure.
func (s *adaptiveEndpointStats) DecrementInflight() {
	for {
		current := s.hot.inflight.Load()
		if current <= 0 {
			return
		}
		if s.hot.inflight.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// ObserveLatency folds one completed RPC into the endpoint latency EWMA.
func (s *adaptiveEndpointStats) ObserveLatency(sample time.Duration) {
	if sample < 0 {
		sample = 0
	}
	old := time.Duration(s.hot.ewmaNanos.Load())
	var updated time.Duration
	if old == 0 {
		updated = sample
	} else {
		updated = time.Duration(0.2*float64(sample) + 0.8*float64(old))
	}
	s.hot.ewmaNanos.Store(uint64(updated))
}

// LatencyEWMA returns latency ewma data for adaptiveEndpointStats callers without handing out mutable receiver state.
func (s *adaptiveEndpointStats) LatencyEWMA() time.Duration {
	return time.Duration(s.hot.ewmaNanos.Load())
}

// LatencySeconds returns latency seconds data for adaptiveEndpointStats callers without handing out mutable receiver state.
func (s *adaptiveEndpointStats) LatencySeconds() float64 {
	return s.LatencyEWMA().Seconds()
}

// adaptiveRandomSource defines the adaptive random source contract used by resolver, picker, and reporter state.
type adaptiveRandomSource interface {
	Intn(n int) int
}

// adaptiveAtomicRandomSource carries adaptive atomic random source state for resolver, picker, and reporter state.
type adaptiveAtomicRandomSource struct {
	state atomic.Uint64
}

// newAdaptiveAtomicRandomSource initializes adaptive atomic random source with package defaults for this package's call path.
func newAdaptiveAtomicRandomSource(seed uint64) *adaptiveAtomicRandomSource {
	if seed == 0 {
		seed = adaptiveRandomIncrement
	}
	source := &adaptiveAtomicRandomSource{}
	source.state.Store(seed)
	return source
}

// Intn returns a concurrency-safe pseudo-random value in [0, n).
func (r *adaptiveAtomicRandomSource) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// next advances a splitmix-style atomic state without sharing math/rand state.
func (r *adaptiveAtomicRandomSource) next() uint64 {
	z := r.state.Add(adaptiveRandomIncrement)
	z = (z ^ (z >> 30)) * uint64(0xbf58476d1ce4e5b9)
	z = (z ^ (z >> 27)) * uint64(0x94d049bb133111eb)
	return z ^ (z >> 31)
}

// maxInt64 keeps max int64 rules consistent for resolver, picker, and reporter state.
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

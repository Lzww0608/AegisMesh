package aegisgrpc

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
)

const adaptiveLimiterPoolTargetKey = "aegis_limiter_pool"

var (
	// adaptiveLimiterPools identifies the adaptive limiter pools constant used by this package.
	adaptiveLimiterPools  sync.Map
	adaptiveLimiterPoolID atomic.Uint64
)

// adaptiveLimiterPool carries adaptive limiter pool state for resolver, picker, and reporter state.
type adaptiveLimiterPool struct {
	max      *circuitbreaker.MaxInflight
	limiters sync.Map
}

// newAdaptiveLimiterPool initializes adaptive limiter pool with package defaults for this package's call path.
func newAdaptiveLimiterPool(max int64) *adaptiveLimiterPool {
	return &adaptiveLimiterPool{max: circuitbreaker.NewMaxInflight(max)}
}

// limiter returns limiter data for adaptiveLimiterPool callers without handing out mutable receiver state.
func (p *adaptiveLimiterPool) limiter(endpoint string) *circuitbreaker.EndpointLimiter {
	if p == nil {
		return circuitbreaker.NewEndpointLimiter(adaptiveDefaultMaxInflightPerTarget)
	}
	endpoint = normalizeAdaptiveLimiterEndpoint(endpoint)
	if value, ok := p.limiters.Load(endpoint); ok {
		return value.(*circuitbreaker.EndpointLimiter)
	}
	limiter := circuitbreaker.NewEndpointLimiterWithMax(p.max)
	value, _ := p.limiters.LoadOrStore(endpoint, limiter)
	return value.(*circuitbreaker.EndpointLimiter)
}

// SetMaxInflightPerEndpoint updates set max inflight per endpoint state while preserving package invariants.
func (p *adaptiveLimiterPool) SetMaxInflightPerEndpoint(max int64) {
	if p == nil || p.max == nil {
		return
	}
	p.max.Set(max)
}

// MaxInflightPerEndpoint returns max inflight per endpoint data for adaptiveLimiterPool callers without handing out mutable receiver state.
func (p *adaptiveLimiterPool) MaxInflightPerEndpoint() int64 {
	if p == nil || p.max == nil {
		return 0
	}
	return p.max.Load()
}

// registerAdaptiveLimiterPool registers register adaptive limiter pool with the controller or local registry.
func registerAdaptiveLimiterPool(pool *adaptiveLimiterPool) string {
	if pool == nil {
		return ""
	}
	id := strconv.FormatUint(adaptiveLimiterPoolID.Add(1), 36)
	adaptiveLimiterPools.Store(id, pool)
	return id
}

// loadAdaptiveLimiterPool reads adaptive limiter pool state from the configured backing source and returns a caller-owned view.
func loadAdaptiveLimiterPool(id string) *adaptiveLimiterPool {
	if id == "" {
		return nil
	}
	value, ok := adaptiveLimiterPools.Load(id)
	if !ok {
		return nil
	}
	pool, _ := value.(*adaptiveLimiterPool)
	return pool
}

// unregisterAdaptiveLimiterPool unregisters unregister adaptive limiter pool and releases its process-local handle.
func unregisterAdaptiveLimiterPool(id string) {
	if id == "" {
		return
	}
	adaptiveLimiterPools.Delete(id)
}

// normalizeAdaptiveLimiterEndpoint normalizes normalize adaptive limiter endpoint so downstream logic sees one canonical form.
func normalizeAdaptiveLimiterEndpoint(endpoint string) string {
	if endpoint == "" {
		return "unknown"
	}
	return endpoint
}

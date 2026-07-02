package aegisgrpc

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
)

const adaptiveLimiterPoolTargetKey = "aegis_limiter_pool"

var (
	adaptiveLimiterPools  sync.Map
	adaptiveLimiterPoolID atomic.Uint64
)

type adaptiveLimiterPool struct {
	max      *circuitbreaker.MaxInflight
	limiters sync.Map
}

func newAdaptiveLimiterPool(max int64) *adaptiveLimiterPool {
	return &adaptiveLimiterPool{max: circuitbreaker.NewMaxInflight(max)}
}

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

func (p *adaptiveLimiterPool) SetMaxInflightPerEndpoint(max int64) {
	if p == nil || p.max == nil {
		return
	}
	p.max.Set(max)
}

func (p *adaptiveLimiterPool) MaxInflightPerEndpoint() int64 {
	if p == nil || p.max == nil {
		return 0
	}
	return p.max.Load()
}

func registerAdaptiveLimiterPool(pool *adaptiveLimiterPool) string {
	if pool == nil {
		return ""
	}
	id := strconv.FormatUint(adaptiveLimiterPoolID.Add(1), 36)
	adaptiveLimiterPools.Store(id, pool)
	return id
}

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

func unregisterAdaptiveLimiterPool(id string) {
	if id == "" {
		return
	}
	adaptiveLimiterPools.Delete(id)
}

func normalizeAdaptiveLimiterEndpoint(endpoint string) string {
	if endpoint == "" {
		return "unknown"
	}
	return endpoint
}

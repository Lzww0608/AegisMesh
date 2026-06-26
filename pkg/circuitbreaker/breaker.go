package circuitbreaker

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/align"
)

var ErrOpen = errors.New("circuit breaker open")

type Config struct {
	MaxInflightPerEndpoint int64
}

type Breaker struct {
	max      int64
	limiters sync.Map
}

type EndpointLimiter struct {
	inflight atomic.Int64
	max      int64
	_        align.Pad48
}

func NewBreaker(cfg Config) *Breaker {
	if cfg.MaxInflightPerEndpoint <= 0 {
		cfg.MaxInflightPerEndpoint = 128
	}
	return &Breaker{max: cfg.MaxInflightPerEndpoint}
}

func NewEndpointLimiter(max int64) *EndpointLimiter {
	if max <= 0 {
		max = 128
	}
	return &EndpointLimiter{max: max}
}

func (l *EndpointLimiter) TryAcquire() bool {
	if l == nil {
		return false
	}
	for {
		current := l.inflight.Load()
		if current >= l.max {
			return false
		}
		if l.inflight.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (l *EndpointLimiter) Release() {
	if l == nil {
		return
	}
	for {
		current := l.inflight.Load()
		if current <= 0 {
			return
		}
		if l.inflight.CompareAndSwap(current, current-1) {
			return
		}
	}
}

func (l *EndpointLimiter) Inflight() int64 {
	if l == nil {
		return 0
	}
	return l.inflight.Load()
}

func (l *EndpointLimiter) Max() int64 {
	if l == nil {
		return 0
	}
	return l.max
}

func (b *Breaker) TryAcquire(endpoint string) error {
	if !b.limiter(endpoint).TryAcquire() {
		return ErrOpen
	}
	return nil
}

func (b *Breaker) Acquire(endpoint string) (func(), error) {
	endpoint = normalizeEndpoint(endpoint)
	limiter := b.limiter(endpoint)
	if !limiter.TryAcquire() {
		return nil, ErrOpen
	}

	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			limiter.Release()
		}
	}, nil
}

func (b *Breaker) Release(endpoint string) {
	if limiter, ok := b.loadLimiter(endpoint); ok {
		limiter.Release()
	}
}

func (b *Breaker) Inflight(endpoint string) int64 {
	if limiter, ok := b.loadLimiter(endpoint); ok {
		return limiter.Inflight()
	}
	return 0
}

func (b *Breaker) limiter(endpoint string) *EndpointLimiter {
	endpoint = normalizeEndpoint(endpoint)
	if value, ok := b.limiters.Load(endpoint); ok {
		return value.(*EndpointLimiter)
	}
	limiter := NewEndpointLimiter(b.max)
	value, _ := b.limiters.LoadOrStore(endpoint, limiter)
	return value.(*EndpointLimiter)
}

func (b *Breaker) loadLimiter(endpoint string) (*EndpointLimiter, bool) {
	value, ok := b.limiters.Load(normalizeEndpoint(endpoint))
	if !ok {
		return nil, false
	}
	return value.(*EndpointLimiter), true
}

func normalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		return "unknown"
	}
	return endpoint
}

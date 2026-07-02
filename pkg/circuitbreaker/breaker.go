package circuitbreaker

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/align"
)

var ErrOpen = errors.New("circuit breaker open")

const defaultMaxInflightPerEndpoint = int64(128)

type Config struct {
	MaxInflightPerEndpoint int64
}

type Breaker struct {
	max      *MaxInflight
	limiters sync.Map
}

type MaxInflight struct {
	value atomic.Int64
}

type EndpointLimiter struct {
	inflight atomic.Int64
	max      *MaxInflight
	_        align.Pad48
}

func NewBreaker(cfg Config) *Breaker {
	return &Breaker{max: NewMaxInflight(cfg.MaxInflightPerEndpoint)}
}

func NewMaxInflight(max int64) *MaxInflight {
	limit := &MaxInflight{}
	limit.Set(max)
	return limit
}

func (m *MaxInflight) Set(max int64) {
	if m == nil {
		return
	}
	m.value.Store(normalizeMaxInflight(max))
}

func (m *MaxInflight) Load() int64 {
	if m == nil {
		return defaultMaxInflightPerEndpoint
	}
	return normalizeMaxInflight(m.value.Load())
}

func NewEndpointLimiter(max int64) *EndpointLimiter {
	return NewEndpointLimiterWithMax(NewMaxInflight(max))
}

func NewEndpointLimiterWithMax(max *MaxInflight) *EndpointLimiter {
	if max == nil {
		max = NewMaxInflight(defaultMaxInflightPerEndpoint)
	}
	return &EndpointLimiter{max: max}
}

func (l *EndpointLimiter) TryAcquire() bool {
	if l == nil {
		return false
	}
	for {
		current := l.inflight.Load()
		if current >= l.max.Load() {
			return false
		}
		if l.inflight.CompareAndSwap(current, current+1) {
			if current+1 <= l.max.Load() {
				return true
			}
			l.Release()
			return false
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
	return l.max.Load()
}

func (l *EndpointLimiter) SetMax(max int64) {
	if l == nil {
		return
	}
	l.max.Set(max)
}

func (b *Breaker) SetMaxInflightPerEndpoint(max int64) {
	if b == nil || b.max == nil {
		return
	}
	b.max.Set(max)
}

func (b *Breaker) MaxInflightPerEndpoint() int64 {
	if b == nil || b.max == nil {
		return 0
	}
	return b.max.Load()
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
	limiter := NewEndpointLimiterWithMax(b.max)
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

func normalizeMaxInflight(max int64) int64 {
	if max <= 0 {
		return defaultMaxInflightPerEndpoint
	}
	return max
}

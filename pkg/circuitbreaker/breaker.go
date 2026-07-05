package circuitbreaker

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/align"
)

var ErrOpen = errors.New("circuit breaker open")

const defaultMaxInflightPerEndpoint = int64(128)

// Config describes the tunables required to initialize this component without call-site defaults.
type Config struct {
	MaxInflightPerEndpoint int64
}

// Breaker carries breaker state for this package call path.
type Breaker struct {
	max      *MaxInflight
	limiters sync.Map
}

// MaxInflight carries max inflight state for this package call path.
type MaxInflight struct {
	value atomic.Int64
}

// EndpointLimiter carries endpoint limiter state for this package call path.
type EndpointLimiter struct {
	inflight atomic.Int64
	max      *MaxInflight
	_        align.Pad48
}

// NewBreaker initializes breaker with package defaults for this package's call path.
func NewBreaker(cfg Config) *Breaker {
	return &Breaker{max: NewMaxInflight(cfg.MaxInflightPerEndpoint)}
}

// NewMaxInflight initializes max inflight with package defaults for this package's call path.
func NewMaxInflight(max int64) *MaxInflight {
	limit := &MaxInflight{}
	limit.Set(max)
	return limit
}

// Set updates set state while preserving package invariants.
func (m *MaxInflight) Set(max int64) {
	if m == nil {
		return
	}
	m.value.Store(normalizeMaxInflight(max))
}

// Load reads the current state from the configured backing source.
func (m *MaxInflight) Load() int64 {
	if m == nil {
		return defaultMaxInflightPerEndpoint
	}
	return normalizeMaxInflight(m.value.Load())
}

// NewEndpointLimiter initializes endpoint limiter with package defaults for this package's call path.
func NewEndpointLimiter(max int64) *EndpointLimiter {
	return NewEndpointLimiterWithMax(NewMaxInflight(max))
}

// NewEndpointLimiterWithMax initializes endpoint limiter with max with package defaults for this package's call path.
func NewEndpointLimiterWithMax(max *MaxInflight) *EndpointLimiter {
	if max == nil {
		max = NewMaxInflight(defaultMaxInflightPerEndpoint)
	}
	return &EndpointLimiter{max: max}
}

// TryAcquire attempts to reserve capacity without blocking the caller.
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

// Release releases previously acquired capacity back to the limiter.
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

// Inflight returns inflight data for EndpointLimiter callers without handing out mutable receiver state.
func (l *EndpointLimiter) Inflight() int64 {
	if l == nil {
		return 0
	}
	return l.inflight.Load()
}

// Max returns max data for EndpointLimiter callers without handing out mutable receiver state.
func (l *EndpointLimiter) Max() int64 {
	if l == nil {
		return 0
	}
	return l.max.Load()
}

// SetMax updates set max state while preserving package invariants.
func (l *EndpointLimiter) SetMax(max int64) {
	if l == nil {
		return
	}
	l.max.Set(max)
}

// SetMaxInflightPerEndpoint updates set max inflight per endpoint state while preserving package invariants.
func (b *Breaker) SetMaxInflightPerEndpoint(max int64) {
	if b == nil || b.max == nil {
		return
	}
	b.max.Set(max)
}

// MaxInflightPerEndpoint returns max inflight per endpoint data for Breaker callers without handing out mutable receiver state.
func (b *Breaker) MaxInflightPerEndpoint() int64 {
	if b == nil || b.max == nil {
		return 0
	}
	return b.max.Load()
}

// TryAcquire attempts to reserve capacity without blocking the caller.
func (b *Breaker) TryAcquire(endpoint string) error {
	if !b.limiter(endpoint).TryAcquire() {
		return ErrOpen
	}
	return nil
}

// Acquire reserves endpoint capacity and returns an idempotent release callback for the caller to invoke.
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

// Release releases previously acquired capacity back to the limiter.
func (b *Breaker) Release(endpoint string) {
	if limiter, ok := b.loadLimiter(endpoint); ok {
		limiter.Release()
	}
}

// Inflight returns inflight data for Breaker callers without handing out mutable receiver state.
func (b *Breaker) Inflight(endpoint string) int64 {
	if limiter, ok := b.loadLimiter(endpoint); ok {
		return limiter.Inflight()
	}
	return 0
}

// limiter returns limiter data for Breaker callers without handing out mutable receiver state.
func (b *Breaker) limiter(endpoint string) *EndpointLimiter {
	endpoint = normalizeEndpoint(endpoint)
	if value, ok := b.limiters.Load(endpoint); ok {
		return value.(*EndpointLimiter)
	}
	limiter := NewEndpointLimiterWithMax(b.max)
	value, _ := b.limiters.LoadOrStore(endpoint, limiter)
	return value.(*EndpointLimiter)
}

// loadLimiter reads limiter state from the configured backing source and returns a caller-owned view.
func (b *Breaker) loadLimiter(endpoint string) (*EndpointLimiter, bool) {
	value, ok := b.limiters.Load(normalizeEndpoint(endpoint))
	if !ok {
		return nil, false
	}
	return value.(*EndpointLimiter), true
}

// normalizeEndpoint normalizes normalize endpoint so downstream logic sees one canonical form.
func normalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		return "unknown"
	}
	return endpoint
}

// normalizeMaxInflight normalizes normalize max inflight so downstream logic sees one canonical form.
func normalizeMaxInflight(max int64) int64 {
	if max <= 0 {
		return defaultMaxInflightPerEndpoint
	}
	return max
}

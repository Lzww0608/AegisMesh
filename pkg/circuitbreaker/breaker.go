package circuitbreaker

import (
	"errors"
	"sync"
)

var ErrOpen = errors.New("circuit breaker open")

type Config struct {
	MaxInflightPerEndpoint int64
}

type Breaker struct {
	mu       sync.Mutex
	max      int64
	inflight map[string]int64
}

func NewBreaker(cfg Config) *Breaker {
	if cfg.MaxInflightPerEndpoint <= 0 {
		cfg.MaxInflightPerEndpoint = 128
	}
	return &Breaker{
		max:      cfg.MaxInflightPerEndpoint,
		inflight: make(map[string]int64),
	}
}

func (b *Breaker) TryAcquire(endpoint string) error {
	endpoint = normalizeEndpoint(endpoint)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.inflight[endpoint] >= b.max {
		return ErrOpen
	}
	b.inflight[endpoint]++
	return nil
}

func (b *Breaker) Acquire(endpoint string) (func(), error) {
	endpoint = normalizeEndpoint(endpoint)
	if err := b.TryAcquire(endpoint); err != nil {
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			b.Release(endpoint)
		})
	}, nil
}

func (b *Breaker) Release(endpoint string) {
	b.release(normalizeEndpoint(endpoint))
}

func (b *Breaker) Inflight(endpoint string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight[normalizeEndpoint(endpoint)]
}

func (b *Breaker) release(endpoint string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inflight[endpoint] <= 1 {
		delete(b.inflight, endpoint)
		return
	}
	b.inflight[endpoint]--
}

func normalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		return "unknown"
	}
	return endpoint
}

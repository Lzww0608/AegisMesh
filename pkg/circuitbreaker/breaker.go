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

func (b *Breaker) Acquire(endpoint string) (func(), error) {
	if endpoint == "" {
		endpoint = "unknown"
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.inflight[endpoint] >= b.max {
		return nil, ErrOpen
	}
	b.inflight[endpoint]++

	var once sync.Once
	return func() {
		once.Do(func() {
			b.release(endpoint)
		})
	}, nil
}

func (b *Breaker) Inflight(endpoint string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight[endpoint]
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

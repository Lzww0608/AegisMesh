package circuitbreaker

import (
	"errors"
	"testing"
)

func TestBreakerLimitsInflightPerEndpoint(t *testing.T) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 2})

	release1, err := breaker.Acquire("user-a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release2, err := breaker.Acquire("user-a")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	_, err = breaker.Acquire("user-a")
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen when inflight limit is reached, got %v", err)
	}

	release1()
	release2()
	release3, err := breaker.Acquire("user-a")
	if err != nil {
		t.Fatalf("expected acquire after release: %v", err)
	}
	release3()
}

func TestBreakerTracksEndpointsIndependently(t *testing.T) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1})

	releaseA, err := breaker.Acquire("user-a")
	if err != nil {
		t.Fatalf("acquire user-a: %v", err)
	}
	defer releaseA()

	releaseB, err := breaker.Acquire("user-b")
	if err != nil {
		t.Fatalf("expected independent budget for user-b: %v", err)
	}
	releaseB()
}

func TestBreakerTryAcquireAndRelease(t *testing.T) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1})

	if err := breaker.TryAcquire("user-a"); err != nil {
		t.Fatalf("first try-acquire: %v", err)
	}
	if err := breaker.TryAcquire("user-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen after max inflight is reached, got %v", err)
	}

	breaker.Release("user-a")
	if got := breaker.Inflight("user-a"); got != 0 {
		t.Fatalf("expected inflight to drop to 0 after release, got %d", got)
	}
	if err := breaker.TryAcquire("user-a"); err != nil {
		t.Fatalf("try-acquire after release: %v", err)
	}
}
func TestEndpointLimiterLimitsInflight(t *testing.T) {
	limiter := NewEndpointLimiter(2)

	if !limiter.TryAcquire() {
		t.Fatalf("first acquire rejected")
	}
	if !limiter.TryAcquire() {
		t.Fatalf("second acquire rejected")
	}
	if limiter.TryAcquire() {
		t.Fatalf("expected acquire to fail after max inflight is reached")
	}
	if got := limiter.Inflight(); got != 2 {
		t.Fatalf("expected inflight 2, got %d", got)
	}

	limiter.Release()
	if got := limiter.Inflight(); got != 1 {
		t.Fatalf("expected inflight 1 after release, got %d", got)
	}
	if !limiter.TryAcquire() {
		t.Fatalf("expected acquire after release")
	}
	limiter.Release()
	limiter.Release()
	limiter.Release()
	if got := limiter.Inflight(); got != 0 {
		t.Fatalf("expected repeated release to stop at 0, got %d", got)
	}
}

func TestEndpointLimiterReadsSharedDynamicMax(t *testing.T) {
	max := NewMaxInflight(1)
	limiter := NewEndpointLimiterWithMax(max)

	if !limiter.TryAcquire() {
		t.Fatalf("first acquire rejected")
	}
	if limiter.TryAcquire() {
		t.Fatalf("expected acquire to fail at initial max")
	}

	max.Set(2)
	if !limiter.TryAcquire() {
		t.Fatalf("expected raised max to allow acquire immediately")
	}

	max.Set(1)
	if limiter.TryAcquire() {
		t.Fatalf("expected lowered max to block new acquire while inflight is above max")
	}
	limiter.Release()
	if limiter.TryAcquire() {
		t.Fatalf("expected lowered max to block while inflight equals max")
	}
	limiter.Release()
	if !limiter.TryAcquire() {
		t.Fatalf("expected acquire after inflight drops below lowered max")
	}
}

func TestBreakerHotAppliesMaxInflightPerEndpoint(t *testing.T) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1})

	if err := breaker.TryAcquire("user-a"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := breaker.TryAcquire("user-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen at initial max, got %v", err)
	}

	breaker.SetMaxInflightPerEndpoint(2)
	if err := breaker.TryAcquire("user-a"); err != nil {
		t.Fatalf("expected raised max to allow acquire immediately: %v", err)
	}

	breaker.SetMaxInflightPerEndpoint(1)
	if err := breaker.TryAcquire("user-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen while inflight is above lowered max, got %v", err)
	}
	breaker.Release("user-a")
	if err := breaker.TryAcquire("user-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen while inflight equals lowered max, got %v", err)
	}
	breaker.Release("user-a")
	if err := breaker.TryAcquire("user-a"); err != nil {
		t.Fatalf("expected acquire after inflight drops below lowered max: %v", err)
	}
}

func TestZeroValueMaxInflightUsesDefaultLimit(t *testing.T) {
	max := &MaxInflight{}
	limiter := NewEndpointLimiterWithMax(max)

	if got := limiter.Max(); got != defaultMaxInflightPerEndpoint {
		t.Fatalf("expected zero-value max to normalize to default %d, got %d", defaultMaxInflightPerEndpoint, got)
	}
	for i := int64(0); i < defaultMaxInflightPerEndpoint; i++ {
		if !limiter.TryAcquire() {
			t.Fatalf("expected acquire %d to succeed below default max", i+1)
		}
	}
	if limiter.TryAcquire() {
		t.Fatalf("expected acquire above default max to fail")
	}
}

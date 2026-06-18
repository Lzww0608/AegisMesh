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

package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFaultProfileInjectsApplicationError(t *testing.T) {
	profile := FaultProfile{
		ErrorProbability: 1,
		RandomFloat:      func() float64 { return 0 },
	}

	err := profile.BeforeCall(context.Background())
	if !errors.Is(err, ErrInjectedFault) {
		t.Fatalf("expected injected fault error, got %v", err)
	}
}

func TestFaultProfileInjectsDelayWithInjectedSleeper(t *testing.T) {
	var slept time.Duration
	profile := FaultProfile{
		SlowProbability: 1,
		SlowDuration:    250 * time.Millisecond,
		RandomFloat:     func() float64 { return 0 },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = d
			return nil
		},
	}

	if err := profile.BeforeCall(context.Background()); err != nil {
		t.Fatalf("before call: %v", err)
	}
	if slept != 250*time.Millisecond {
		t.Fatalf("expected 250ms injected delay, got %s", slept)
	}
}

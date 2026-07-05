package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFaultProfileInjectsApplicationError locks the fault profile injects application error contract so future changes do not regress it.
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

// TestFaultProfileInjectsDelayWithInjectedSleeper locks the fault profile injects delay with injected sleeper contract so future changes do not regress it.
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

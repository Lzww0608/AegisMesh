package services

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

var ErrInjectedFault = errors.New("injected application fault")

// FaultProfile describes optional demo fault injection before a service handler runs.
type FaultProfile struct {
	SlowProbability  float64
	SlowDuration     time.Duration
	ErrorProbability float64
	RandomFloat      func() float64
	Sleep            func(context.Context, time.Duration) error
}

// BeforeCall injects configured delay or error faults and respects context cancellation during sleeps.
func (p FaultProfile) BeforeCall(ctx context.Context) error {
	randomFloat := p.RandomFloat
	if randomFloat == nil {
		randomFloat = rand.Float64
	}
	if p.ErrorProbability > 0 && randomFloat() < p.ErrorProbability {
		return ErrInjectedFault
	}
	if p.SlowProbability > 0 && p.SlowDuration > 0 && randomFloat() < p.SlowProbability {
		sleep := p.Sleep
		if sleep == nil {
			sleep = sleepContext
		}
		return sleep(ctx, p.SlowDuration)
	}
	return nil
}

// sleepContext waits for the configured delay while allowing request cancellation to win.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

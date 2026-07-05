package telemetry

import (
	"math"
	"time"
)

const defaultEWMAAlpha = 0.2

// EWMA carries ewma state for recorder aggregation.
type EWMA struct {
	alpha float64
	value float64
	count int64
}

// NewEWMA initializes ewma with package defaults for this package's call path.
func NewEWMA(alpha float64) *EWMA {
	if alpha <= 0 || alpha > 1 || math.IsNaN(alpha) {
		alpha = defaultEWMAAlpha
	}
	return &EWMA{alpha: alpha}
}

// Observe observes observe and folds it into the current aggregate.
func (e *EWMA) Observe(sample time.Duration) {
	if sample < 0 {
		sample = 0
	}
	value := float64(sample)
	if e.count == 0 {
		e.value = value
	} else {
		e.value = e.alpha*value + (1-e.alpha)*e.value
	}
	e.count++
}

// Value returns value data for EWMA callers without handing out mutable receiver state.
func (e *EWMA) Value() time.Duration {
	return time.Duration(math.Round(e.value))
}

// ValueSeconds returns value seconds data for EWMA callers without handing out mutable receiver state.
func (e *EWMA) ValueSeconds() float64 {
	return e.Value().Seconds()
}

// Count returns count data for EWMA callers without handing out mutable receiver state.
func (e *EWMA) Count() int64 {
	return e.count
}

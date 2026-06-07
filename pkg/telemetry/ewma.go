package telemetry

import (
	"math"
	"time"
)

const defaultEWMAAlpha = 0.2

type EWMA struct {
	alpha float64
	value float64
	count int64
}

func NewEWMA(alpha float64) *EWMA {
	if alpha <= 0 || alpha > 1 || math.IsNaN(alpha) {
		alpha = defaultEWMAAlpha
	}
	return &EWMA{alpha: alpha}
}

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

func (e *EWMA) Value() time.Duration {
	return time.Duration(math.Round(e.value))
}

func (e *EWMA) ValueSeconds() float64 {
	return e.Value().Seconds()
}

func (e *EWMA) Count() int64 {
	return e.count
}

package telemetry

import (
	"testing"
	"time"
)

func TestEWMAUsesFirstSampleAsInitialValue(t *testing.T) {
	ewma := NewEWMA(0.2)

	ewma.Observe(100 * time.Millisecond)

	if got := ewma.Value(); got != 100*time.Millisecond {
		t.Fatalf("expected initial EWMA to equal first sample, got %s", got)
	}
	if ewma.Count() != 1 {
		t.Fatalf("expected count 1, got %d", ewma.Count())
	}
}

func TestEWMAAppliesAlphaToLaterSamples(t *testing.T) {
	ewma := NewEWMA(0.2)

	ewma.Observe(100 * time.Millisecond)
	ewma.Observe(200 * time.Millisecond)

	if got := ewma.Value(); got != 120*time.Millisecond {
		t.Fatalf("expected EWMA 120ms, got %s", got)
	}
}

package ebpf

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// TestAggregatorMapsTCPEventsToEndpointSamples locks the aggregator maps tcp events to endpoint samples contract so future changes do not regress it.
func TestAggregatorMapsTCPEventsToEndpointSamples(t *testing.T) {
	agg := NewAggregator(map[string]EndpointRef{
		"10.0.0.2:7001": {Service: "user-service", InstanceID: "user-a", Address: "10.0.0.2:7001"},
	})

	agg.Observe(TCPEvent{
		RemoteKey:      packEndpoint(0x0200000a, 7001),
		Retransmits:    3,
		ConnectErrors:  1,
		ConnectLatency: 25 * time.Millisecond,
		ObservedAt:     time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC),
	})

	samples := agg.SnapshotAndReset()
	if len(samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(samples))
	}
	if samples[0].Service != "user-service" || samples[0].InstanceID != "user-a" {
		t.Fatalf("unexpected endpoint mapping: %+v", samples[0])
	}
	if samples[0].TCPRetransmit != 3 || samples[0].ConnectError != 1 {
		t.Fatalf("unexpected network counters: %+v", samples[0])
	}
	if remaining := agg.SnapshotAndReset(); len(remaining) != 0 {
		t.Fatalf("expected snapshot reset, got %+v", remaining)
	}
}

// TestNewCollectorReportsUnsupportedPlatformOutsideLinux locks the new collector reports unsupported platform outside linux contract so future changes do not regress it.
func TestNewCollectorReportsUnsupportedPlatformOutsideLinux(t *testing.T) {
	collector, err := NewCollector(Config{})
	if runtime.GOOS == "linux" {
		if err != nil {
			t.Skipf("linux eBPF collector unavailable in this environment: %v", err)
		}
		if collector == nil {
			t.Fatalf("expected linux collector")
		}
		return
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("expected ErrUnsupportedPlatform on %s, got %v", runtime.GOOS, err)
	}
	if collector == nil {
		t.Fatalf("expected non-nil fallback collector")
	}
}

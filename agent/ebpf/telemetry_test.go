package ebpf

import (
	"testing"
	"time"
)

// TestNetworkSamplesToTelemetrySamplesPreservesNetworkSignals locks the network samples to telemetry samples preserves network signals contract so future changes do not regress it.
func TestNetworkSamplesToTelemetrySamplesPreservesNetworkSignals(t *testing.T) {
	windowStart := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Second)

	got := NetworkSamplesToTelemetrySamples([]NetworkSample{
		{
			Service:       "user-service",
			InstanceID:    "user-a",
			Address:       "10.0.0.2:7001",
			TCPRetransmit: 3,
			ConnectError:  1,
			WindowStart:   windowStart,
			WindowEnd:     windowEnd,
		},
	})

	if len(got) != 1 {
		t.Fatalf("expected one telemetry sample, got %d", len(got))
	}
	if got[0].Service != "user-service" || got[0].InstanceId != "user-a" || got[0].EndpointAddress != "10.0.0.2:7001" {
		t.Fatalf("unexpected sample identity: %+v", got[0])
	}
	if got[0].Source != "ebpf" {
		t.Fatalf("expected ebpf telemetry source, got %q", got[0].Source)
	}
	if got[0].TcpRetransmit != 3 || got[0].ConnectError != 1 {
		t.Fatalf("expected network counters to be preserved, got %+v", got[0])
	}
	if got[0].WindowStartUnixMillis != windowStart.UnixMilli() || got[0].WindowEndUnixMillis != windowEnd.UnixMilli() {
		t.Fatalf("expected window timestamps to be preserved, got %+v", got[0])
	}
}

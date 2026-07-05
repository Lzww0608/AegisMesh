package telemetry

import (
	"runtime"
	"testing"
	"time"
)

// TestEndpointStatsSlicePoolReusesCapacity locks the endpoint stats slice pool reuses capacity contract so future changes do not regress it.
func TestEndpointStatsSlicePoolReusesCapacity(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)

	for endpointStatsSlicePool.Get() != nil {
	}

	const attempts = 64
	for i := 0; i < attempts; i++ {
		first := acquireEndpointStatsSlice(128)
		first = append(first, EndpointStats{Source: "frontend", Destination: "user-service"})
		ReleaseEndpointStatsSlice(first)

		second := acquireEndpointStatsSlice(64)
		if cap(second) >= 128 {
			if len(second) != 0 {
				t.Fatalf("expected empty slice from pool, got len=%d", len(second))
			}
			ReleaseEndpointStatsSlice(second)
			return
		}
		ReleaseEndpointStatsSlice(second)
	}
	t.Fatalf("pool never returned >=128 cap slice across %d attempts (sync.Pool may be dropping puts)", attempts)
}

// TestSnapshotAndResetRequiresRelease locks the snapshot and reset requires release contract so future changes do not regress it.
func TestSnapshotAndResetRequiresRelease(t *testing.T) {
	recorder := NewRecorder("frontend", nil)
	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo/Get",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     10 * time.Millisecond,
	})

	stats := recorder.SnapshotAndReset()
	if len(stats) != 1 {
		t.Fatalf("expected one stats row, got %d", len(stats))
	}
	ReleaseEndpointStatsSlice(stats)
}

// TestSnapshotReturnsIndependentCopy locks the snapshot returns independent copy contract so future changes do not regress it.
func TestSnapshotReturnsIndependentCopy(t *testing.T) {
	recorder := NewRecorder("frontend", nil)
	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo/Get",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     10 * time.Millisecond,
	})

	stats := recorder.Snapshot()
	if len(stats) != 1 {
		t.Fatalf("expected one stats row, got %d", len(stats))
	}
	stats[0].Destination = "mutated"
	fresh := recorder.Snapshot()
	if fresh[0].Destination == "mutated" {
		t.Fatalf("expected Snapshot copy to be independent of caller mutations")
	}
}

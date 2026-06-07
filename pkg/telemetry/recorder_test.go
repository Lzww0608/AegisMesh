package telemetry

import (
	"testing"
	"time"
)

func TestRecorderAggregatesEndpointWindowStats(t *testing.T) {
	recorder := NewRecorder("frontend", nil)

	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	})
	recorder.Observe(Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "DEADLINE_EXCEEDED",
		Latency:     300 * time.Millisecond,
		Error:       true,
		Timeout:     true,
	})

	stats := recorder.SnapshotAndReset()
	if len(stats) != 1 {
		t.Fatalf("expected one endpoint stats row, got %d", len(stats))
	}
	got := stats[0]
	if got.Source != "frontend" || got.Destination != "user-service" || got.Upstream != "127.0.0.1:7001" {
		t.Fatalf("unexpected stats identity: %+v", got)
	}
	if got.RequestCount != 2 || got.ErrorCount != 1 || got.TimeoutCount != 1 {
		t.Fatalf("unexpected request counters: %+v", got)
	}
	if got.LatencyEWMA != 140*time.Millisecond {
		t.Fatalf("expected EWMA 140ms, got %s", got.LatencyEWMA)
	}
	if got.LatencyP95 != 300*time.Millisecond {
		t.Fatalf("expected p95 300ms, got %s", got.LatencyP95)
	}

	if remaining := recorder.SnapshotAndReset(); len(remaining) != 0 {
		t.Fatalf("expected snapshot to reset window, got %+v", remaining)
	}
}

func TestRecorderTracksInflightAroundCalls(t *testing.T) {
	recorder := NewRecorder("frontend", nil)
	finish := recorder.Start("user-service", "/demo.shop.v1.UserService/GetUser", "127.0.0.1:7001")

	active := recorder.Snapshot()
	if len(active) != 1 {
		t.Fatalf("expected active stats row, got %d", len(active))
	}
	if active[0].Inflight != 1 {
		t.Fatalf("expected inflight 1, got %d", active[0].Inflight)
	}

	finish("OK")
	done := recorder.Snapshot()
	if len(done) != 1 {
		t.Fatalf("expected finished stats row, got %d", len(done))
	}
	if done[0].Inflight != 0 || done[0].RequestCount != 1 {
		t.Fatalf("expected finished call to decrement inflight and record request, got %+v", done[0])
	}
}

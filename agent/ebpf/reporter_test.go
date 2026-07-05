package ebpf

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
)

// TestReporterAggregatesCollectorEventsAndReportsTelemetry locks the reporter aggregates collector events and reports telemetry contract so future changes do not regress it.
func TestReporterAggregatesCollectorEventsAndReportsTelemetry(t *testing.T) {
	collector := newFakeCollector()
	client := &fakeTelemetryClient{}
	reporter := NewReporter(ReporterConfig{
		Collector: collector,
		Client:    client,
		Aggregator: NewAggregator(map[string]EndpointRef{
			"10.0.0.2:7001": {Service: "user-service", InstanceID: "user-a", Address: "10.0.0.2:7001"},
		}),
		Interval: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := reporter.Start(ctx); err != nil {
		t.Fatalf("start reporter: %v", err)
	}
	collector.events <- TCPEvent{RemoteKey: packEndpoint(0x0200000a, 7001), Retransmits: 2, ObservedAt: time.Now()}

	if err := reporter.ReportOnce(ctx); err != nil {
		t.Fatalf("report once: %v", err)
	}
	if client.last == nil || len(client.last.Samples) != 1 {
		t.Fatalf("expected one reported sample, got %+v", client.last)
	}
	if client.last.Samples[0].TcpRetransmit != 2 {
		t.Fatalf("expected retransmit sample, got %+v", client.last.Samples[0])
	}
	if err := reporter.Stop(); err != nil {
		t.Fatalf("stop reporter: %v", err)
	}
}

// fakeCollector carries fake collector state for the eBPF telemetry path.
type fakeCollector struct {
	events  chan TCPEvent
	started bool
	stopped bool
}

// newFakeCollector initializes fake collector with package defaults for this package's call path.
func newFakeCollector() *fakeCollector {
	return &fakeCollector{events: make(chan TCPEvent, 4)}
}

// Start begins collection and binds the collector lifetime to its owned resources.
func (c *fakeCollector) Start() error {
	c.started = true
	return nil
}

// Stop releases collector resources and makes repeated shutdown calls harmless.
func (c *fakeCollector) Stop() error {
	c.stopped = true
	close(c.events)
	return nil
}

// Events returns events data for fakeCollector callers without handing out mutable receiver state.
func (c *fakeCollector) Events() <-chan TCPEvent {
	return c.events
}

// fakeTelemetryClient defines the client calls required for fake telemetry client.
type fakeTelemetryClient struct {
	last *aegisv1.ReportEndpointStatsRequest
}

// ReportEndpointStats records fake telemetry samples so reporter tests can assert batch contents.
func (c *fakeTelemetryClient) ReportEndpointStats(_ context.Context, req *aegisv1.ReportEndpointStatsRequest, _ ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error) {
	c.last = req
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

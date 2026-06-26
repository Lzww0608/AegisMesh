package ebpf

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
)

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

type fakeCollector struct {
	events  chan TCPEvent
	started bool
	stopped bool
}

func newFakeCollector() *fakeCollector {
	return &fakeCollector{events: make(chan TCPEvent, 4)}
}

func (c *fakeCollector) Start() error {
	c.started = true
	return nil
}

func (c *fakeCollector) Stop() error {
	c.stopped = true
	close(c.events)
	return nil
}

func (c *fakeCollector) Events() <-chan TCPEvent {
	return c.events
}

type fakeTelemetryClient struct {
	last *aegisv1.ReportEndpointStatsRequest
}

func (c *fakeTelemetryClient) ReportEndpointStats(_ context.Context, req *aegisv1.ReportEndpointStatsRequest, _ ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error) {
	c.last = req
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

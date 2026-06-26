package aegisgrpc

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestTelemetryReporterSendsAndResetsRecorderWindow(t *testing.T) {
	client := &fakeTelemetryClient{}
	recorder := telemetry.NewRecorder("frontend", nil)
	recorder.Observe(telemetry.Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     100 * time.Millisecond,
	})
	reporter := newTelemetryReporter(client, recorder, time.Minute)

	if err := reporter.ReportOnce(context.Background()); err != nil {
		t.Fatalf("report telemetry: %v", err)
	}

	if client.last == nil || len(client.last.Samples) != 1 {
		t.Fatalf("expected one reported sample, got %+v", client.last)
	}
	sample := client.last.Samples[0]
	if sample.Source != "frontend" || sample.Service != "user-service" || sample.InstanceId != "user-a" || sample.EndpointAddress != "127.0.0.1:7001" {
		t.Fatalf("unexpected reported sample identity: %+v", sample)
	}
	if sample.RequestCount != 1 || sample.LatencyP95Seconds <= 0 || sample.LatencyEwmaSeconds <= 0 {
		t.Fatalf("unexpected reported sample stats: %+v", sample)
	}
	remaining := recorder.SnapshotAndReset()
	defer telemetry.ReleaseEndpointStatsSlice(remaining)
	if len(remaining) != 0 {
		t.Fatalf("expected reporter to reset recorder window, got %+v", remaining)
	}
}

type fakeTelemetryClient struct {
	last *aegisv1.ReportEndpointStatsRequest
}

func (c *fakeTelemetryClient) ReportEndpointStats(_ context.Context, req *aegisv1.ReportEndpointStatsRequest, _ ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error) {
	clone := &aegisv1.ReportEndpointStatsRequest{
		Samples: make([]*aegisv1.EndpointStatsSample, len(req.Samples)),
	}
	for i, sample := range req.Samples {
		if sample == nil {
			continue
		}
		clone.Samples[i] = proto.Clone(sample).(*aegisv1.EndpointStatsSample)
	}
	c.last = clone
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

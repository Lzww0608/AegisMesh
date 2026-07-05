package aegisgrpc

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
)

// benchTelemetryClient defines the client calls required for bench telemetry client.
type benchTelemetryClient struct{}

// ReportEndpointStats consumes benchmark samples without adding controller I/O to reporter timings.
func (benchTelemetryClient) ReportEndpointStats(context.Context, *aegisv1.ReportEndpointStatsRequest, ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error) {
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

// BenchmarkTelemetryReporterReportOnce reports latency and allocation cost for telemetry reporter report once.
func BenchmarkTelemetryReporterReportOnce(b *testing.B) {
	recorder := telemetry.NewRecorder("frontend", nil)
	reporter := newTelemetryReporter(benchTelemetryClient{}, recorder, time.Minute)
	ctx := context.Background()

	methods := []string{
		"/demo.shop.v1.UserService/GetUser",
		"/demo.shop.v1.UserService/ListUsers",
		"/demo.shop.v1.OrderService/GetOrder",
	}
	upstreams := []string{"10.0.0.1:7001", "10.0.0.2:7001", "10.0.0.3:7001"}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j, method := range methods {
			recorder.Observe(telemetry.Observation{
				Destination: "user-service",
				Method:      method,
				Upstream:    upstreams[j%len(upstreams)],
				Status:      "OK",
				Latency:     time.Millisecond,
			})
		}
		if err := reporter.ReportOnce(ctx); err != nil {
			b.Fatalf("report: %v", err)
		}
	}
}

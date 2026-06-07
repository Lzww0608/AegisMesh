package aegisgrpc

import (
	"context"
	"testing"

	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTelemetryUnaryInterceptorRecordsFailedRPC(t *testing.T) {
	recorder := telemetry.NewRecorder("frontend", nil)
	interceptor := newTelemetryUnaryInterceptor("user-service", recorder)

	err := interceptor(
		context.Background(),
		"/demo.shop.v1.UserService/GetUser",
		nil,
		nil,
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return status.Error(codes.Unavailable, "backend down")
		},
	)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected unavailable error, got %v", err)
	}

	stats := recorder.Snapshot()
	if len(stats) != 1 {
		t.Fatalf("expected one stats row, got %d", len(stats))
	}
	if stats[0].Destination != "user-service" || stats[0].Method != "/demo.shop.v1.UserService/GetUser" {
		t.Fatalf("unexpected stats identity: %+v", stats[0])
	}
	if stats[0].RequestCount != 1 || stats[0].ErrorCount != 1 {
		t.Fatalf("expected one failed request, got %+v", stats[0])
	}
}

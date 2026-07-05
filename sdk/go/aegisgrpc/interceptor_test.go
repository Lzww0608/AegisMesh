package aegisgrpc

import (
	"context"
	"os"
	"testing"

	"github.com/aegismesh/aegismesh/pkg/telemetry"
	tracepkg "github.com/aegismesh/aegismesh/pkg/trace"
	"github.com/aegismesh/aegismesh/pkg/verifier"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestTelemetryUnaryInterceptorRecordsFailedRPC locks the telemetry unary interceptor records failed rpc contract so future changes do not regress it.
func TestTelemetryUnaryInterceptorRecordsFailedRPC(t *testing.T) {
	recorder := telemetry.NewRecorder("frontend", nil)
	interceptor := newTelemetryUnaryInterceptor("frontend", "user-service", recorder, nil)

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

// TestTelemetryUnaryInterceptorWritesVerifierTrace locks the telemetry unary interceptor writes verifier trace contract so future changes do not regress it.
func TestTelemetryUnaryInterceptorWritesVerifierTrace(t *testing.T) {
	path := t.TempDir() + "/aegis-traces.jsonl"
	tracer, err := tracepkg.NewDefaultAsyncJSONLWriter(path)
	if err != nil {
		t.Fatalf("new trace writer: %v", err)
	}
	defer tracer.Close()

	ctx := ContextWithTraceID(context.Background(), "trace-1")
	ctx = contextWithAttempt(ctx, 2)
	interceptor := newTelemetryUnaryInterceptor("frontend", "user-service", nil, tracer)
	err = interceptor(
		ctx,
		"/demo.shop.v1.UserService/GetUser",
		nil,
		nil,
		nil,
		func(ctx context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatalf("expected outgoing trace metadata")
			}
			if got := md.Get("x-aegis-trace-id"); len(got) != 1 || got[0] != "trace-1" {
				t.Fatalf("expected trace id metadata, got %+v", md)
			}
			if got := md.Get("x-aegis-attempt"); len(got) != 1 || got[0] != "2" {
				t.Fatalf("expected attempt metadata, got %+v", md)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("close trace writer: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace output: %v", err)
	}
	defer file.Close()
	traces, err := verifier.LoadTraceJSONL(file)
	if err != nil {
		t.Fatalf("load traces: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected one trace record, got %d", len(traces))
	}
	if traces[0].TraceID != "trace-1" || traces[0].Route != "user-service@unknown" || traces[0].RetryAttempts != 1 {
		t.Fatalf("unexpected trace record: %+v", traces[0])
	}
	if len(traces[0].Path) != 2 || traces[0].Path[0] != "frontend" || traces[0].Path[1] != "user-service@unknown" {
		t.Fatalf("unexpected trace path: %+v", traces[0].Path)
	}
}

package aegisgrpc

import (
	"context"
	"time"

	"github.com/aegismesh/aegismesh/pkg/telemetry"
	tracepkg "github.com/aegismesh/aegismesh/pkg/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// newTelemetryUnaryInterceptor initializes telemetry unary interceptor with package defaults for this package's call path.
func newTelemetryUnaryInterceptor(source, destination string, recorder *telemetry.Recorder, tracer tracepkg.Writer) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if recorder == nil && tracer == nil {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		start := time.Now()
		ctx = ensureTraceID(ctx)
		spanID := newSpanID()
		ctx = contextWithTraceMetadata(ctx, spanID)
		var remote peer.Peer
		opts = append(opts, grpc.Peer(&remote))
		err := invoker(ctx, method, req, reply, cc, opts...)
		code := status.Code(err)
		upstream := "unknown"
		endpointID := ""
		if remote.Addr != nil {
			upstream = remote.Addr.String()
			endpointID = endpointIDForAddress(upstream)
		}

		statusValue := code.String()
		if recorder != nil {
			recorder.Observe(telemetry.Observation{
				Destination: destination,
				Method:      method,
				EndpointID:  endpointID,
				Upstream:    upstream,
				Status:      statusValue,
				Latency:     time.Since(start),
				Error:       code != codes.OK,
				Timeout:     code == codes.DeadlineExceeded,
			})
		}
		if tracer != nil {
			route := destination + "@" + upstream
			_ = tracer.Write(tracepkg.Record{
				TraceID:       traceIDFromContext(ctx),
				SpanID:        spanID,
				Source:        source,
				Destination:   destination,
				Method:        method,
				Route:         route,
				Path:          []string{source, route},
				Upstream:      upstream,
				Attempt:       attemptFromContext(ctx),
				RetryAttempts: attemptFromContext(ctx) - 1,
				Status:        statusValue,
			})
		}
		return err
	}
}

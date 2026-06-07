package aegisgrpc

import (
	"context"
	"time"

	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func newTelemetryUnaryInterceptor(destination string, recorder *telemetry.Recorder) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if recorder == nil {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		start := time.Now()
		var remote peer.Peer
		opts = append(opts, grpc.Peer(&remote))
		err := invoker(ctx, method, req, reply, cc, opts...)
		code := status.Code(err)
		upstream := "unknown"
		if remote.Addr != nil {
			upstream = remote.Addr.String()
		}

		recorder.Observe(telemetry.Observation{
			Destination: destination,
			Method:      method,
			Upstream:    upstream,
			Status:      code.String(),
			Latency:     time.Since(start),
			Error:       code != codes.OK,
			Timeout:     code == codes.DeadlineExceeded,
		})
		return err
	}
}

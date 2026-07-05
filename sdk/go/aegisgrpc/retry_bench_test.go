package aegisgrpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var benchmarkRetryInterceptorErr = status.Error(codes.Unavailable, "try again")

// BenchmarkRetryUnaryInterceptorRetryableFailure reports latency and allocation cost for retry unary interceptor retryable failure.
func BenchmarkRetryUnaryInterceptorRetryableFailure(b *testing.B) {
	interceptor := newRetryUnaryInterceptor(RetryPolicy{
		MaxAttempts:    2,
		RetryableCodes: []codes.Code{codes.Unavailable},
	}, nil)
	ctx := ContextWithTraceID(context.Background(), "trace-bench")
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return benchmarkRetryInterceptorErr
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = interceptor(ctx, "/demo.shop.v1.UserService/GetUser", nil, nil, nil, invoker)
	}
}

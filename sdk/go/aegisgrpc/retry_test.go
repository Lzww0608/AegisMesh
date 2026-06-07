package aegisgrpc

import (
	"context"
	"testing"
	"time"

	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRetryUnaryInterceptorRetriesRetryableErrorWithinBudget(t *testing.T) {
	budget := retrypkg.NewBudget(retrypkg.BudgetConfig{
		BudgetRatio: 1,
		MinBudget:   1,
		Window:      time.Minute,
	})
	interceptor := newRetryUnaryInterceptor(RetryPolicy{
		MaxAttempts:    2,
		PerTryTimeout:  time.Second,
		RetryableCodes: []codes.Code{codes.Unavailable},
	}, budget)

	attempts := 0
	err := interceptor(
		context.Background(),
		"/demo.shop.v1.UserService/GetUser",
		nil,
		nil,
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			attempts++
			if attempts == 1 {
				return status.Error(codes.Unavailable, "try again")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
}

func TestRetryUnaryInterceptorStopsWhenBudgetExhausted(t *testing.T) {
	budget := retrypkg.NewBudget(retrypkg.BudgetConfig{
		BudgetRatio: 0,
		MinBudget:   0,
		Window:      time.Minute,
	})
	interceptor := newRetryUnaryInterceptor(RetryPolicy{
		MaxAttempts:    3,
		PerTryTimeout:  time.Second,
		RetryableCodes: []codes.Code{codes.Unavailable},
	}, budget)

	attempts := 0
	err := interceptor(
		context.Background(),
		"/demo.shop.v1.UserService/GetUser",
		nil,
		nil,
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			attempts++
			return status.Error(codes.Unavailable, "try again")
		},
	)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected original unavailable error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected no retry when budget is exhausted, got %d attempts", attempts)
	}
}

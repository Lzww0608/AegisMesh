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

// TestCompileRetryPolicyBuildsRetryableBitset locks the compile retry policy builds retryable bitset contract so future changes do not regress it.
func TestCompileRetryPolicyBuildsRetryableBitset(t *testing.T) {
	retry := compileRetryPolicy(RetryPolicy{
		MaxAttempts:    3,
		PerTryTimeout:  time.Second,
		RetryableCodes: []codes.Code{codes.Unavailable, codes.DeadlineExceeded},
	})

	if retry.maxAttempts != 3 || retry.perTryTimeout != time.Second {
		t.Fatalf("unexpected compiled retry timing/attempts: %+v", retry)
	}
	if retry.retryableMask == 0 {
		t.Fatalf("expected non-empty retryable bitset")
	}
	if !retry.IsRetryable(codes.Unavailable) || !retry.IsRetryable(codes.DeadlineExceeded) {
		t.Fatalf("expected unavailable and deadline exceeded to be retryable")
	}
	if retry.IsRetryable(codes.InvalidArgument) {
		t.Fatalf("expected invalid argument to be non-retryable")
	}
}

// TestCompileRetryPolicyUsesDefaultRetryableBitset locks the compile retry policy uses default retryable bitset contract so future changes do not regress it.
func TestCompileRetryPolicyUsesDefaultRetryableBitset(t *testing.T) {
	retry := compileRetryPolicy(RetryPolicy{MaxAttempts: 2})
	if !retry.IsRetryable(codes.Unavailable) || !retry.IsRetryable(codes.DeadlineExceeded) {
		t.Fatalf("expected default retryable codes to match the SDK defaults")
	}
	if retry.IsRetryable(codes.InvalidArgument) {
		t.Fatalf("expected invalid argument to remain non-retryable by default")
	}

	allocs := testing.AllocsPerRun(1000, func() {
		if !retry.IsRetryable(codes.Unavailable) {
			t.Fatalf("expected unavailable to be retryable")
		}
	})
	if allocs != 0 {
		t.Fatalf("expected retryable bitset lookup to allocate 0 times, got %.2f", allocs)
	}
}

// TestRetryUnaryInterceptorRetriesRetryableErrorWithinBudget locks the retry unary interceptor retries retryable error within budget contract so future changes do not regress it.
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

// TestRetryUnaryInterceptorBoundsAmplificationAtBudgetRatio locks the retry unary interceptor bounds amplification at budget ratio contract so future changes do not regress it.
func TestRetryUnaryInterceptorBoundsAmplificationAtBudgetRatio(t *testing.T) {
	budget := retrypkg.NewBudget(retrypkg.BudgetConfig{
		BudgetRatio: 0.15,
		MinBudget:   0,
		Window:      time.Minute,
	})
	interceptor := newRetryUnaryInterceptor(RetryPolicy{
		MaxAttempts:    2,
		RetryableCodes: []codes.Code{codes.Unavailable},
	}, budget)

	attempts := 0
	for i := 0; i < 1000; i++ {
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
			t.Fatalf("expected unavailable error, got %v", err)
		}
	}

	if attempts != 1150 {
		t.Fatalf("expected 1.150x amplification from 1000 originals and 150 retries, got %d attempts", attempts)
	}
	snapshot := budget.Snapshot()
	if snapshot.OriginalRequests != 1000 || snapshot.RetryRequests != 150 || snapshot.AllowedRetries != 150 {
		t.Fatalf("unexpected retry budget snapshot: %+v", snapshot)
	}
}

// TestRetryUnaryInterceptorStopsWhenBudgetExhausted locks the retry unary interceptor stops when budget exhausted contract so future changes do not regress it.
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

// TestRetryUnaryInterceptorAnnotatesAttemptContext locks the retry unary interceptor annotates attempt context contract so future changes do not regress it.
func TestRetryUnaryInterceptorAnnotatesAttemptContext(t *testing.T) {
	interceptor := newRetryUnaryInterceptor(RetryPolicy{
		MaxAttempts:    2,
		PerTryTimeout:  time.Second,
		RetryableCodes: []codes.Code{codes.Unavailable},
	}, nil)

	var attempts []int
	var traceIDs []string
	err := interceptor(
		context.Background(),
		"/demo.shop.v1.UserService/GetUser",
		nil,
		nil,
		nil,
		func(ctx context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			attempts = append(attempts, attemptFromContext(ctx))
			traceIDs = append(traceIDs, traceIDFromContext(ctx))
			if len(attempts) == 1 {
				return status.Error(codes.Unavailable, "try again")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("expected attempt context [1 2], got %+v", attempts)
	}
	if traceIDs[0] == "" || traceIDs[0] != traceIDs[1] {
		t.Fatalf("expected stable trace id across attempts, got %+v", traceIDs)
	}
}

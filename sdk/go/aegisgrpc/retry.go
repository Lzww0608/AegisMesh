package aegisgrpc

import (
	"context"
	"time"

	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RetryPolicy struct {
	MaxAttempts    int
	PerTryTimeout  time.Duration
	RetryableCodes []codes.Code
}

type retryPolicySource interface {
	PolicyForMethod(method string) (RetryPolicy, *retrypkg.Budget)
}

type staticRetrySource struct {
	policy RetryPolicy
	budget *retrypkg.Budget
}

func (s staticRetrySource) PolicyForMethod(string) (RetryPolicy, *retrypkg.Budget) {
	return s.policy, s.budget
}

func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    2,
		PerTryTimeout:  750 * time.Millisecond,
		RetryableCodes: []codes.Code{codes.Unavailable, codes.DeadlineExceeded},
	}
}

func newRetryUnaryInterceptor(policy RetryPolicy, budget *retrypkg.Budget) grpc.UnaryClientInterceptor {
	return newRetryUnaryInterceptorFromSource(staticRetrySource{policy: policy, budget: budget})
}

func newRetryUnaryInterceptorFromSource(source retryPolicySource) grpc.UnaryClientInterceptor {
	if source == nil {
		source = staticRetrySource{policy: defaultRetryPolicy()}
	}

	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		policy, budget := source.PolicyForMethod(method)
		return invokeWithRetryPolicy(ctx, method, req, reply, cc, invoker, policy, budget, opts...)
	}
}

func invokeWithRetryPolicy(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, policy RetryPolicy, budget *retrypkg.Budget, opts ...grpc.CallOption) error {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	if len(policy.RetryableCodes) == 0 {
		policy.RetryableCodes = defaultRetryPolicy().RetryableCodes
	}
	retryable := make(map[codes.Code]struct{}, len(policy.RetryableCodes))
	for _, code := range policy.RetryableCodes {
		retryable[code] = struct{}{}
	}

	ctx = ensureTraceID(ctx)
	if budget != nil {
		budget.RecordOriginal()
	}

	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx := ctx
		cancel := func() {}
		if policy.PerTryTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, policy.PerTryTimeout)
		}
		attemptCtx = contextWithAttempt(attemptCtx, attempt)
		err := invoker(attemptCtx, method, req, reply, cc, opts...)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == policy.MaxAttempts {
			break
		}
		if _, ok := retryable[status.Code(err)]; !ok {
			break
		}
		if budget != nil {
			if !budget.AllowRetry() {
				break
			}
			budget.RecordRetry()
		}
	}
	return lastErr
}

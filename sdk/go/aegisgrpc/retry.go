package aegisgrpc

import (
	"context"
	"time"

	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultRetryableMask = (uint64(1) << uint(codes.Unavailable)) | (uint64(1) << uint(codes.DeadlineExceeded))

// RetryPolicy describes retry policy rules distributed through the control plane.
type RetryPolicy struct {
	MaxAttempts    int
	PerTryTimeout  time.Duration
	RetryableCodes []codes.Code
}

// compiledRetry carries compiled retry state for resolver, picker, and reporter state.
type compiledRetry struct {
	mode          RetryMode
	maxAttempts   int
	perTryTimeout time.Duration
	retryableMask uint64
	budget        retrypkg.BudgetConfig
}

// compileRetryPolicy provides the shared compile retry policy helper for resolver, picker, and reporter state.
func compileRetryPolicy(policy RetryPolicy) compiledRetry {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	mask := retryableCodeMask(policy.RetryableCodes)
	if len(policy.RetryableCodes) == 0 {
		mask = defaultRetryableMask
	}
	return compiledRetry{
		maxAttempts:   policy.MaxAttempts,
		perTryTimeout: policy.PerTryTimeout,
		retryableMask: mask,
	}
}

// retryableCodeMask provides the shared retryable code mask helper for resolver, picker, and reporter state.
func retryableCodeMask(retryableCodes []codes.Code) uint64 {
	var mask uint64
	for _, code := range retryableCodes {
		mask |= retryableCodeBit(code)
	}
	return mask
}

// retryableCodeBit provides the shared retryable code bit helper for resolver, picker, and reporter state.
func retryableCodeBit(code codes.Code) uint64 {
	shift := uint(code)
	if shift >= 64 {
		return 0
	}
	return uint64(1) << shift
}

// IsRetryable returns is retryable data for compiledRetry callers without handing out mutable receiver state.
func (r compiledRetry) IsRetryable(code codes.Code) bool {
	return r.retryableMask&retryableCodeBit(code) != 0
}

// toPolicy returns to policy data for compiledRetry callers without handing out mutable receiver state.
func (r compiledRetry) toPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    r.maxAttempts,
		PerTryTimeout:  r.perTryTimeout,
		RetryableCodes: retryableCodesFromMask(r.retryableMask),
	}
}

// retryableCodesFromMask provides the shared retryable codes from mask helper for resolver, picker, and reporter state.
func retryableCodesFromMask(mask uint64) []codes.Code {
	if mask == 0 {
		return nil
	}
	out := make([]codes.Code, 0, 4)
	for code := uint(0); code < 64; code++ {
		if mask&(uint64(1)<<code) != 0 {
			out = append(out, codes.Code(code))
		}
	}
	return out
}

// retryPolicySource defines the retry policy source contract used by resolver, picker, and reporter state.
type retryPolicySource interface {
	PolicyForMethod(method string) (compiledRetry, *retrypkg.Budget)
}

// staticRetrySource carries static retry source state for resolver, picker, and reporter state.
type staticRetrySource struct {
	retry  compiledRetry
	budget *retrypkg.Budget
}

// PolicyForMethod returns policy for method data for staticRetrySource callers without handing out mutable receiver state.
func (s staticRetrySource) PolicyForMethod(string) (compiledRetry, *retrypkg.Budget) {
	return s.retry, s.budget
}

// defaultRetryPolicy keeps default retry policy rules consistent for resolver, picker, and reporter state.
func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    2,
		PerTryTimeout:  750 * time.Millisecond,
		RetryableCodes: []codes.Code{codes.Unavailable, codes.DeadlineExceeded},
	}
}

// newRetryUnaryInterceptor initializes retry unary interceptor with package defaults for this package's call path.
func newRetryUnaryInterceptor(policy RetryPolicy, budget *retrypkg.Budget) grpc.UnaryClientInterceptor {
	return newRetryUnaryInterceptorFromSource(staticRetrySource{retry: compileRetryPolicy(policy), budget: budget})
}

// newRetryUnaryInterceptorFromSource initializes retry unary interceptor from source with package defaults for this package's call path.
func newRetryUnaryInterceptorFromSource(source retryPolicySource) grpc.UnaryClientInterceptor {
	if source == nil {
		source = staticRetrySource{retry: compileRetryPolicy(defaultRetryPolicy())}
	}

	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		retry, budget := source.PolicyForMethod(method)
		return invokeWithRetryPolicy(ctx, method, req, reply, cc, invoker, retry, budget, opts...)
	}
}

// invokeWithRetryPolicy provides the shared invoke with retry policy helper for resolver, picker, and reporter state.
func invokeWithRetryPolicy(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, retry compiledRetry, budget *retrypkg.Budget, opts ...grpc.CallOption) error {
	if retry.maxAttempts <= 0 {
		retry.maxAttempts = 1
	}
	ctx = ensureTraceID(ctx)
	if budget != nil {
		budget.RecordOriginal()
	}

	var lastErr error
	for attempt := 1; attempt <= retry.maxAttempts; attempt++ {
		attemptCtx := ctx
		cancel := func() {}
		if retry.perTryTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, retry.perTryTimeout)
		}
		attemptCtx = contextWithAttempt(attemptCtx, attempt)
		err := invoker(attemptCtx, method, req, reply, cc, opts...)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == retry.maxAttempts {
			break
		}
		if !retry.IsRetryable(status.Code(err)) {
			break
		}
		if budget != nil && !budget.TryAcquireRetry() {
			break
		}
	}
	return lastErr
}

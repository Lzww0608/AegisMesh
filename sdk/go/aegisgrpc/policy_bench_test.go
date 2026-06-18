package aegisgrpc

import (
	"fmt"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	retrypkg "github.com/aegismesh/aegismesh/pkg/retry"
	"google.golang.org/grpc/codes"
)

var (
	benchmarkRetryPolicy RetryPolicy
	benchmarkRetryBudget *retrypkg.Budget
)

func BenchmarkDynamicRetrySourcePolicyForMethodParallel(b *testing.B) {
	methodCounts := []int{1, 16, 128}

	for _, methods := range methodCounts {
		b.Run(fmt.Sprintf("methods=%d", methods), func(b *testing.B) {
			methodNames := benchmarkMethodNames(methods)
			source := newDynamicRetrySource(DialOptions{
				RoutingPolicy: RoutingAdaptiveP2C,
				RetryMode:     RetryBudget,
				RetryPolicy: RetryPolicy{
					MaxAttempts:    2,
					PerTryTimeout:  750 * time.Millisecond,
					RetryableCodes: []codes.Code{codes.Unavailable, codes.DeadlineExceeded},
				},
				RetryBudget: retrypkg.BudgetConfig{
					BudgetRatio: 0.15,
					MinBudget:   10,
					Window:      10 * time.Second,
				},
			}, &policyManager{})
			source.Update(benchmarkPolicySnapshotForMethods(methodNames))
			for _, method := range methodNames {
				source.PolicyForMethod(method)
			}

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					benchmarkRetryPolicy, benchmarkRetryBudget = source.PolicyForMethod(methodNames[i%len(methodNames)])
					i++
				}
			})
		})
	}
}

func benchmarkMethodNames(methods int) []string {
	names := make([]string, methods)
	for i := range names {
		names[i] = fmt.Sprintf("/bench.Service/Method%d", i)
	}
	return names
}

func benchmarkPolicySnapshotForMethods(methods []string) *aegisv1.PolicySnapshot {
	policies := make(map[string]*aegisv1.MethodPolicy, len(methods))
	for _, method := range methods {
		policies[method] = &aegisv1.MethodPolicy{
			Method:        method,
			Idempotent:    true,
			TimeoutMillis: 150,
			Retry: &aegisv1.RetryPolicy{
				Enabled:             true,
				MaxAttempts:         2,
				BudgetRatio:         0.15,
				MinBudget:           10,
				WindowSeconds:       10,
				PerTryTimeoutMillis: 100,
			},
		}
	}
	return &aegisv1.PolicySnapshot{
		Service:       "bench-service",
		Revision:      1,
		RoutingPolicy: string(RoutingAdaptiveP2C),
		Retry: &aegisv1.RetryPolicy{
			Enabled:             true,
			MaxAttempts:         2,
			BudgetRatio:         0.15,
			MinBudget:           10,
			WindowSeconds:       10,
			PerTryTimeoutMillis: 750,
		},
		Methods: policies,
	}
}

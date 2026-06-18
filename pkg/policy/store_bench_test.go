package policy

import (
	"fmt"
	"os"
	"strings"
	"testing"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
)

var benchmarkPolicySnapshot *aegisv1.PolicySnapshot

func BenchmarkFileStoreGetPolicySnapshot(b *testing.B) {
	methodCounts := []int{1, 16, 128}

	for _, methods := range methodCounts {
		b.Run(fmt.Sprintf("methods=%d", methods), func(b *testing.B) {
			path := writeBenchmarkPolicyFile(b, methods)
			store, err := NewFileStore(path)
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var ok bool
				benchmarkPolicySnapshot, ok = store.Get("bench-service")
				if !ok {
					b.Fatal("expected bench-service policy")
				}
			}
		})
	}
}

func writeBenchmarkPolicyFile(b *testing.B, methods int) string {
	b.Helper()

	var builder strings.Builder
	builder.WriteString(`
services:
  bench-service:
    routing_policy: adaptive_p2c
    retry:
      enabled: true
      max_attempts: 2
      budget_ratio: 0.15
      min_budget: 10
      window_seconds: 10
      per_try_timeout_millis: 750
    outlier_detection:
      degraded_threshold: 1.5
      eject_threshold: 2.5
      consecutive_windows: 3
      ejection_duration_seconds: 30
      recovery_threshold: 1.0
      probe_success_threshold: 0.95
    circuit_breaker:
      max_inflight_per_endpoint: 128
    methods:
`)
	for i := 0; i < methods; i++ {
		fmt.Fprintf(&builder, "      /bench.Service/Method%d:\n", i)
		builder.WriteString("        idempotent: true\n")
		builder.WriteString("        timeout_millis: 150\n")
		builder.WriteString("        retry:\n")
		builder.WriteString("          enabled: true\n")
		builder.WriteString("          max_attempts: 2\n")
		builder.WriteString("          per_try_timeout_millis: 100\n")
	}

	path := b.TempDir() + "/policy.yaml"
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	return path
}

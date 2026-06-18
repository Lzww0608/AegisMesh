package circuitbreaker

import (
	"fmt"
	"testing"
)

func BenchmarkBreakerAcquireReleaseSameEndpointParallel(b *testing.B) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1 << 60})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := breaker.TryAcquire("user-service-1"); err != nil {
				panic(err)
			}
			breaker.Release("user-service-1")
		}
	})
}

func BenchmarkBreakerAcquireReleaseManyEndpointsParallel(b *testing.B) {
	endpoints := make([]string, 64)
	for i := range endpoints {
		endpoints[i] = fmt.Sprintf("user-service-%d", i)
	}
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1 << 60})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			endpoint := endpoints[i%len(endpoints)]
			if err := breaker.TryAcquire(endpoint); err != nil {
				panic(err)
			}
			breaker.Release(endpoint)
			i++
		}
	})
}

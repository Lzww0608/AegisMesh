package circuitbreaker

import "testing"

func BenchmarkBreakerSameEndpoint(b *testing.B) {
	limiter := NewEndpointLimiter(1 << 60)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !limiter.TryAcquire() {
				panic(ErrOpen)
			}
			limiter.Release()
		}
	})
}

func BenchmarkBreakerManyEndpoint(b *testing.B) {
	limiters := make([]*EndpointLimiter, 64)
	for i := range limiters {
		limiters[i] = NewEndpointLimiter(1 << 60)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			limiter := limiters[i%len(limiters)]
			if !limiter.TryAcquire() {
				panic(ErrOpen)
			}
			limiter.Release()
			i++
		}
	})
}
func BenchmarkBreakerAPISameEndpoint(b *testing.B) {
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1 << 60})
	if err := breaker.TryAcquire("user-service-1"); err != nil {
		b.Fatal(err)
	}
	breaker.Release("user-service-1")

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

func BenchmarkBreakerAPIManyEndpoint(b *testing.B) {
	endpoints := make([]string, 64)
	breaker := NewBreaker(Config{MaxInflightPerEndpoint: 1 << 60})
	for i := range endpoints {
		endpoints[i] = "user-service-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := breaker.TryAcquire(endpoints[i]); err != nil {
			b.Fatal(err)
		}
		breaker.Release(endpoints[i])
	}

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

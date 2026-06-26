package aegisgrpc

import (
	"testing"
)

func BenchmarkAdaptiveEndpointHotSharedParallel(b *testing.B) {
	stats := make([]*adaptiveEndpointStats, 1)
	stats[0] = &adaptiveEndpointStats{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			stats[0].IncrementInflight()
			stats[0].ObserveLatency(750)
			stats[0].DecrementInflight()
		}
	})
}

func BenchmarkAdaptiveEndpointHotManyParallel(b *testing.B) {
	const endpoints = 64
	stats := make([]*adaptiveEndpointStats, endpoints)
	for i := range stats {
		stats[i] = &adaptiveEndpointStats{}
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			stat := stats[i%endpoints]
			stat.IncrementInflight()
			stat.ObserveLatency(750)
			stat.DecrementInflight()
			i++
		}
	})
}

package ebpf

import (
	"fmt"
	"testing"
	"time"
)

var benchmarkNetworkSamples []NetworkSample

func BenchmarkAggregatorObserveParallel(b *testing.B) {
	endpointCounts := []int{1, 8, 64}

	for _, endpoints := range endpointCounts {
		b.Run(fmt.Sprintf("endpoints=%d", endpoints), func(b *testing.B) {
			events := makeBenchmarkTCPEvents(endpoints, endpoints)
			agg := NewAggregator(makeBenchmarkEndpointRefs(endpoints))

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					agg.Observe(events[i%len(events)])
					i++
				}
			})
		})
	}
}

func BenchmarkAggregatorSnapshotAndReset(b *testing.B) {
	endpointCounts := []int{1, 8, 64}
	observationCounts := []int{1_000, 10_000, 100_000}

	for _, endpoints := range endpointCounts {
		for _, observations := range observationCounts {
			name := fmt.Sprintf("endpoints=%d/observations=%d", endpoints, observations)
			b.Run(name, func(b *testing.B) {
				events := makeBenchmarkTCPEvents(endpoints, observations)

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					agg := NewAggregator(makeBenchmarkEndpointRefs(endpoints))
					for _, event := range events {
						agg.Observe(event)
					}
					b.StartTimer()
					benchmarkNetworkSamples = agg.SnapshotAndReset()
					b.StopTimer()
					if len(benchmarkNetworkSamples) != endpoints {
						b.Fatalf("expected %d endpoint samples, got %d", endpoints, len(benchmarkNetworkSamples))
					}
					b.StartTimer()
				}
			})
		}
	}
}

func makeBenchmarkEndpointRefs(endpoints int) map[string]EndpointRef {
	refs := make(map[string]EndpointRef, endpoints)
	for i := 0; i < endpoints; i++ {
		address := fmt.Sprintf("10.0.0.%d:7001", i+1)
		refs[address] = EndpointRef{
			Service:    "bench-service",
			InstanceID: fmt.Sprintf("bench-%d", i),
			Address:    address,
		}
	}
	return refs
}

func makeBenchmarkTCPEvents(endpoints, observations int) []TCPEvent {
	events := make([]TCPEvent, observations)
	observedAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for i := range events {
		events[i] = TCPEvent{
			RemoteKey:      benchmarkEndpointKey(i, endpoints),
			Retransmits:    int64(i & 1),
			ConnectErrors:  int64((i >> 1) & 1),
			ConnectLatency: time.Duration(i%1000+1) * time.Microsecond,
			ObservedAt:     observedAt.Add(time.Duration(i) * time.Microsecond),
		}
	}
	return events
}

func benchmarkEndpointKey(index, endpoints int) EndpointKey {
	host := uint32((index%endpoints)+1)<<24 | 0x0a
	return packEndpoint(host, 7001)
}

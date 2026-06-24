package aegisgrpc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"
)

func BenchmarkAdaptivePick_N2(b *testing.B)   { benchmarkAdaptivePick(b, 2) }
func BenchmarkAdaptivePick_N8(b *testing.B)   { benchmarkAdaptivePick(b, 8) }
func BenchmarkAdaptivePick_N32(b *testing.B)  { benchmarkAdaptivePick(b, 32) }
func BenchmarkAdaptivePick_N128(b *testing.B) { benchmarkAdaptivePick(b, 128) }

func benchmarkAdaptivePick(b *testing.B, endpointCount int) {
	mixes := []struct {
		name          string
		degradedRatio float64
		probingRatio  float64
	}{
		{name: "healthy", degradedRatio: 0, probingRatio: 0},
		{name: "degraded25", degradedRatio: 0.25, probingRatio: 0},
		{name: "probing25", degradedRatio: 0, probingRatio: 0.25},
		{name: "degraded25_probing10", degradedRatio: 0.25, probingRatio: 0.10},
	}

	for _, mix := range mixes {
		b.Run(mix.name, func(b *testing.B) {
			picker := newBenchmarkAdaptivePicker(endpointCount, mix.degradedRatio, mix.probingRatio)
			info := balancer.PickInfo{Ctx: context.Background()}

			allocs := testing.AllocsPerRun(1000, func() {
				result, err := picker.Pick(info)
				if err != nil {
					panic(err)
				}
				result.Done(balancer.DoneInfo{})
			})
			b.ReportMetric(allocs, "allocs/pick")

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					result, err := picker.Pick(info)
					if err != nil {
						panic(err)
					}
					result.Done(balancer.DoneInfo{})
				}
			})
		})
	}
}

func newBenchmarkAdaptivePicker(endpointCount int, degradedRatio, probingRatio float64) *adaptivePicker {
	adaptiveStats = sync.Map{}

	ready := make(map[balancer.SubConn]base.SubConnInfo, endpointCount)
	degraded := int(float64(endpointCount) * degradedRatio)
	probing := int(float64(endpointCount) * probingRatio)
	if degraded+probing > endpointCount {
		probing = endpointCount - degraded
	}

	for i := 0; i < endpointCount; i++ {
		status := "HEALTHY"
		slowScore := 0.05 * float64(i%8)
		switch {
		case i < degraded:
			status = "DEGRADED"
			slowScore += 1.5
		case i < degraded+probing:
			status = "PROBING"
		}

		address := fmt.Sprintf("127.0.%d.%d:7%03d", i/255, i%255+1, i)
		subConn := &fakeSubConn{id: fmt.Sprintf("endpoint-%d", i)}
		ready[subConn] = base.SubConnInfo{
			Address: resolver.Address{
				Addr:       address,
				Attributes: addressAttributes(subConn.id, status, slowScore),
			},
		}
	}

	picker := adaptivePickerBuilder{random: newAdaptiveAtomicRandomSource(1)}.
		Build(base.PickerBuildInfo{ReadySCs: ready}).(*adaptivePicker)
	for i := range picker.items {
		picker.items[i].limiter = circuitbreaker.NewEndpointLimiter(int64(endpointCount * 4096))
		picker.items[i].stats.ObserveLatency(time.Duration(i%10+1) * time.Millisecond)
	}
	return picker
}

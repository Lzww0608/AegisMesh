package aegisgrpc

import (
	"fmt"
	"sync"
	"testing"

	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
)

func TestAdaptivePickerChoosesLowerCostReadySubConn(t *testing.T) {
	fast := &fakeSubConn{id: "fast"}
	slow := &fakeSubConn{id: "slow"}
	builder := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0, 1}}}

	picker := builder.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			fast: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("fast", "HEALTHY", 0.1)}},
			slow: {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("slow", "HEALTHY", 3.0)}},
		},
	})

	result, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick subconn: %v", err)
	}
	if result.SubConn != fast {
		t.Fatalf("expected fast subconn, got %+v", result.SubConn)
	}
	if result.Done == nil {
		t.Fatalf("expected done callback to update endpoint stats")
	}
	result.Done(balancer.DoneInfo{})
}

func TestAdaptivePickerSamplesProbingEndpointsOnlyWithinProbeBudget(t *testing.T) {
	healthy := &fakeSubConn{id: "healthy"}
	probe := &fakeSubConn{id: "probe"}

	avoidProbe := adaptivePickerBuilder{random: &sequenceRandom{values: []int{99, 0, 0}}}.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			healthy: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("healthy", "HEALTHY", 0.1)}},
			probe:   {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("probe", "PROBING", 0.0)}},
		},
	})

	result, err := avoidProbe.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick without probe sample: %v", err)
	}
	if result.SubConn != healthy {
		t.Fatalf("expected healthy subconn outside probe budget, got %+v", result.SubConn)
	}
	result.Done(balancer.DoneInfo{})

	useProbe := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0}}}.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			healthy: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("healthy", "HEALTHY", 0.1)}},
			probe:   {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("probe", "PROBING", 0.0)}},
		},
	})

	result, err = useProbe.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick within probe budget: %v", err)
	}
	if result.SubConn != probe {
		t.Fatalf("expected probing subconn when sampled, got %+v", result.SubConn)
	}
	result.Done(balancer.DoneInfo{})
}

func TestAdaptivePickerConcurrentPicks(t *testing.T) {
	adaptiveStats = sync.Map{}

	ready := make(map[balancer.SubConn]base.SubConnInfo, 8)
	for i := 0; i < 8; i++ {
		subConn := &fakeSubConn{id: fmt.Sprintf("endpoint-%d", i)}
		ready[subConn] = base.SubConnInfo{
			Address: resolver.Address{
				Addr:       fmt.Sprintf("127.0.0.1:%d", 7001+i),
				Attributes: addressAttributes(subConn.id, "HEALTHY", 0.1),
			},
		}
	}

	picker := adaptivePickerBuilder{}.Build(base.PickerBuildInfo{ReadySCs: ready})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 256; j++ {
				result, err := picker.Pick(balancer.PickInfo{})
				if err != nil {
					t.Errorf("concurrent pick failed: %v", err)
					return
				}
				result.Done(balancer.DoneInfo{})
			}
		}()
	}
	wg.Wait()
}

func TestAdaptivePickerRejectsWhenEndpointLimiterIsFull(t *testing.T) {
	subConn := &fakeSubConn{id: "limited"}
	picker := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0}}}.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			subConn: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("limited", "HEALTHY", 0.1)}},
		},
	}).(*adaptivePicker)
	picker.items[0].limiter = circuitbreaker.NewEndpointLimiter(1)

	first, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	_, err = picker.Pick(balancer.PickInfo{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted when limiter is full, got %v", err)
	}

	first.Done(balancer.DoneInfo{})
	second, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick after done: %v", err)
	}
	second.Done(balancer.DoneInfo{})
}

type fakeSubConn struct {
	balancer.SubConn
	id string
}

type sequenceRandom struct {
	values []int
	next   int
}

func (r *sequenceRandom) Intn(n int) int {
	if n <= 0 || len(r.values) == 0 {
		return 0
	}
	value := r.values[r.next%len(r.values)]
	r.next++
	return value % n
}

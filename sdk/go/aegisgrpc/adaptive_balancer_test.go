package aegisgrpc

import (
	"fmt"
	"sync"
	"testing"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/circuitbreaker"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	grpcstatus "google.golang.org/grpc/status"
)

func TestAdaptivePickerChoosesLowerCostReadySubConn(t *testing.T) {
	fast := &fakeSubConn{id: "fast"}
	slow := &fakeSubConn{id: "slow"}
	builder := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0, 1}}}

	picker := builder.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			fast: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("fast", aegisstatus.Healthy, 0.1)}},
			slow: {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("slow", aegisstatus.Healthy, 3.0)}},
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
			healthy: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("healthy", aegisstatus.Healthy, 0.1)}},
			probe:   {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("probe", aegisstatus.Probing, 0.0)}},
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
			healthy: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("healthy", aegisstatus.Healthy, 0.1)}},
			probe:   {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("probe", aegisstatus.Probing, 0.0)}},
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
				Attributes: addressAttributes(subConn.id, aegisstatus.Healthy, 0.1),
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
			subConn: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("limited", aegisstatus.Healthy, 0.1)}},
		},
	}).(*adaptivePicker)
	picker.items[0].limiter = circuitbreaker.NewEndpointLimiter(1)

	first, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	_, err = picker.Pick(balancer.PickInfo{})
	if grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted when limiter is full, got %v", err)
	}

	first.Done(balancer.DoneInfo{})
	second, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick after done: %v", err)
	}
	second.Done(balancer.DoneInfo{})
}

func TestAdaptivePickerUsesLimiterPoolAcrossPickerRebuilds(t *testing.T) {
	pool := newAdaptiveLimiterPool(1)
	subConn := &fakeSubConn{id: "limited"}
	info := base.PickerBuildInfo{ReadySCs: map[balancer.SubConn]base.SubConnInfo{
		subConn: {Address: resolver.Address{
			Addr:       "127.0.0.1:7001",
			Attributes: addressAttributesWithLimiterPool("limited", aegisstatus.Healthy, 0.1, pool),
		}},
	}}

	firstPicker := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0}}}.Build(info)
	secondPicker := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0}}}.Build(info)

	first, err := firstPicker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	if _, err := secondPicker.Pick(balancer.PickInfo{}); grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected rebuilt picker to share limiter state, got %v", err)
	}

	first.Done(balancer.DoneInfo{})
	second, err := secondPicker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("pick after shared release: %v", err)
	}
	second.Done(balancer.DoneInfo{})
}

func TestAdaptivePickerHotAppliesLimiterPoolMax(t *testing.T) {
	pool := newAdaptiveLimiterPool(1)
	subConn := &fakeSubConn{id: "limited"}
	picker := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0}}}.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			subConn: {Address: resolver.Address{
				Addr:       "127.0.0.1:7001",
				Attributes: addressAttributesWithLimiterPool("limited", aegisstatus.Healthy, 0.1, pool),
			}},
		},
	})

	first, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	if _, err := picker.Pick(balancer.PickInfo{}); grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted at initial max, got %v", err)
	}

	pool.SetMaxInflightPerEndpoint(2)
	second, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("expected raised max to allow second pick: %v", err)
	}

	pool.SetMaxInflightPerEndpoint(1)
	if _, err := picker.Pick(balancer.PickInfo{}); grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected lowered max to block while inflight is above max, got %v", err)
	}
	first.Done(balancer.DoneInfo{})
	if _, err := picker.Pick(balancer.PickInfo{}); grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected lowered max to block while inflight equals max, got %v", err)
	}
	second.Done(balancer.DoneInfo{})
	third, err := picker.Pick(balancer.PickInfo{})
	if err != nil {
		t.Fatalf("expected pick after inflight drops below lowered max: %v", err)
	}
	third.Done(balancer.DoneInfo{})
}

func TestPolicyManagerHotAppliesCircuitBreakerMax(t *testing.T) {
	pool := newAdaptiveLimiterPool(1)
	manager := &policyManager{circuitBreaker: pool}

	manager.Update(&aegisv1.PolicySnapshot{CircuitBreaker: &aegisv1.CircuitBreakerPolicy{MaxInflightPerEndpoint: 3}})
	if got := pool.MaxInflightPerEndpoint(); got != 3 {
		t.Fatalf("expected max inflight 3 after policy update, got %d", got)
	}
	compiled := manager.Load()
	if compiled == nil || compiled.circuitBreaker.maxInflightPerEndpoint != 3 {
		t.Fatalf("expected compiled circuit breaker policy, got %+v", compiled)
	}

	manager.Update(&aegisv1.PolicySnapshot{})
	if got := pool.MaxInflightPerEndpoint(); got != adaptiveDefaultMaxInflightPerTarget {
		t.Fatalf("expected missing circuit policy to restore default max, got %d", got)
	}
}

func TestAdaptivePickerConcurrentPicksWithLimiterMaxUpdates(t *testing.T) {
	pool := newAdaptiveLimiterPool(64)
	ready := make(map[balancer.SubConn]base.SubConnInfo, 4)
	for i := 0; i < 4; i++ {
		subConn := &fakeSubConn{id: fmt.Sprintf("endpoint-%d", i)}
		ready[subConn] = base.SubConnInfo{Address: resolver.Address{
			Addr:       fmt.Sprintf("127.0.0.1:%d", 7100+i),
			Attributes: addressAttributesWithLimiterPool(subConn.id, aegisstatus.Healthy, 0.1, pool),
		}}
	}
	picker := adaptivePickerBuilder{}.Build(base.PickerBuildInfo{ReadySCs: ready})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 128; j++ {
				result, err := picker.Pick(balancer.PickInfo{})
				if err == nil {
					result.Done(balancer.DoneInfo{})
					continue
				}
				if grpcstatus.Code(err) != codes.ResourceExhausted {
					t.Errorf("unexpected pick error: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			pool.SetMaxInflightPerEndpoint(int64(i%8 + 1))
		}
	}()
	close(start)
	wg.Wait()
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

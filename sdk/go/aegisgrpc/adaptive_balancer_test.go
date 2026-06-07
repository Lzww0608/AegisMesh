package aegisgrpc

import (
	"testing"

	"github.com/aegismesh/aegismesh/pkg/routing"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"
)

func TestAdaptivePickerChoosesLowerCostReadySubConn(t *testing.T) {
	fast := &fakeSubConn{id: "fast"}
	slow := &fakeSubConn{id: "slow"}
	builder := adaptivePickerBuilder{random: &sequenceRandom{values: []int{0, 1}}}

	picker := builder.Build(base.PickerBuildInfo{
		ReadySCs: map[balancer.SubConn]base.SubConnInfo{
			fast: {Address: resolver.Address{Addr: "127.0.0.1:7001", Attributes: addressAttributes("fast", string(routing.EndpointHealthy), 0.1)}},
			slow: {Address: resolver.Address{Addr: "127.0.0.1:7002", Attributes: addressAttributes("slow", string(routing.EndpointHealthy), 3.0)}},
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

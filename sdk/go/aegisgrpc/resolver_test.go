package aegisgrpc

import (
	"net/url"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

func TestRegistryResolverNextWatchRetryDelayCapsBackoff(t *testing.T) {
	r := &registryResolver{
		refreshInterval: defaultRefreshInterval,
		watchBackoff:    defaultRefreshInterval,
	}

	got := []time.Duration{
		r.nextWatchRetryDelay(),
		r.nextWatchRetryDelay(),
		r.nextWatchRetryDelay(),
		r.nextWatchRetryDelay(),
	}
	want := []time.Duration{
		3 * time.Second,
		6 * time.Second,
		12 * time.Second,
		24 * time.Second,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff[%d] = %s, want %s", i, got[i], want[i])
		}
	}

	r.watchBackoff = 20 * time.Second
	if delay := r.nextWatchRetryDelay(); delay != 20*time.Second {
		t.Fatalf("expected capped delay 20s, got %s", delay)
	}
	if r.watchBackoff != maxWatchRetryBackoff {
		t.Fatalf("expected backoff cap %s, got %s", maxWatchRetryBackoff, r.watchBackoff)
	}
}

func TestTargetForServiceBuildsAegisTarget(t *testing.T) {
	got := TargetForService("127.0.0.1:9000", "user-service")
	if got != "aegis://127.0.0.1:9000/user-service" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestParseTargetExtractsControllerAndService(t *testing.T) {
	u, err := url.Parse("aegis://127.0.0.1:9000/user-service")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	controller, service, err := parseTarget(resolver.Target{URL: *u})
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if controller != "127.0.0.1:9000" {
		t.Fatalf("expected controller address, got %s", controller)
	}
	if service != "user-service" {
		t.Fatalf("expected service name, got %s", service)
	}
}

func TestInstancesToAddressesKeepsOnlyHealthyInstances(t *testing.T) {
	got := instancesToAddresses([]*aegisv1.ServiceInstance{
		{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
		{Id: "user-b", Address: "127.0.0.1:7002", Status: "DEAD"},
		{Id: "user-c", Address: "127.0.0.1:7003"},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 routable addresses, got %d", len(got))
	}
	if got[0].Addr != "127.0.0.1:7001" || got[1].Addr != "127.0.0.1:7003" {
		t.Fatalf("unexpected addresses: %+v", got)
	}
}

func TestInstancesToAddressesAttachesAegisAttributes(t *testing.T) {
	got := instancesToAddresses([]*aegisv1.ServiceInstance{
		{Id: "user-a", Address: "127.0.0.1:7001", Status: "DEGRADED", SlowScore: 1.75},
	})

	if len(got) != 1 {
		t.Fatalf("expected one address, got %d", len(got))
	}
	if instanceIDFromAttributes(got[0].Attributes) != "user-a" {
		t.Fatalf("expected instance id attribute user-a")
	}
	if endpointStatusFromAttributes(got[0].Attributes) != aegisstatus.Degraded {
		t.Fatalf("expected status attribute DEGRADED")
	}
	if slowScoreFromAttributes(got[0].Attributes) != 1.75 {
		t.Fatalf("expected slow score attribute 1.75")
	}
}

func TestInstancesToAddressesCachesEndpointIdentityForTelemetry(t *testing.T) {
	instancesToAddresses([]*aegisv1.ServiceInstance{
		{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
	})

	if got := endpointIDForAddress("127.0.0.1:7001"); got != "user-a" {
		t.Fatalf("expected endpoint id user-a, got %q", got)
	}
}

func TestRegistryResolverSkipsUnchangedVersion(t *testing.T) {
	cc := &recordingResolverClientConn{}
	r := &registryResolver{cc: cc}

	first := &aegisv1.ListInstancesResponse{
		Version: 10,
		Instances: []*aegisv1.ServiceInstance{
			{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
		},
	}
	if err := r.applyInstancesResponse(first); err != nil {
		t.Fatalf("apply first response: %v", err)
	}
	if cc.updateCount() != 1 {
		t.Fatalf("expected first response to update state, got %d updates", cc.updateCount())
	}

	if err := r.applyInstancesResponse(&aegisv1.ListInstancesResponse{
		Version: 10,
		Instances: []*aegisv1.ServiceInstance{
			{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
			{Id: "user-b", Address: "127.0.0.1:7002", Status: "HEALTHY"},
		},
	}); err != nil {
		t.Fatalf("apply unchanged version: %v", err)
	}
	if cc.updateCount() != 1 {
		t.Fatalf("unchanged version should not update state, got %d updates", cc.updateCount())
	}

	if err := r.applyInstancesResponse(&aegisv1.ListInstancesResponse{
		Version: 11,
		Instances: []*aegisv1.ServiceInstance{
			{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
			{Id: "user-b", Address: "127.0.0.1:7002", Status: "HEALTHY"},
		},
	}); err != nil {
		t.Fatalf("apply changed version: %v", err)
	}
	if cc.updateCount() != 2 {
		t.Fatalf("changed version should update state, got %d updates", cc.updateCount())
	}
}

func TestRegistryResolverKeepsUpdatingWhenVersionIsMissing(t *testing.T) {
	cc := &recordingResolverClientConn{}
	r := &registryResolver{cc: cc}
	resp := &aegisv1.ListInstancesResponse{
		Instances: []*aegisv1.ServiceInstance{{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"}},
	}

	if err := r.applyInstancesResponse(resp); err != nil {
		t.Fatalf("apply first legacy response: %v", err)
	}
	if err := r.applyInstancesResponse(resp); err != nil {
		t.Fatalf("apply second legacy response: %v", err)
	}
	if cc.updateCount() != 2 {
		t.Fatalf("legacy version=0 responses should preserve polling behavior, got %d updates", cc.updateCount())
	}
}

type recordingResolverClientConn struct {
	states []resolver.State
	errors []error
}

func (c *recordingResolverClientConn) UpdateState(state resolver.State) error {
	c.states = append(c.states, state)
	return nil
}

func (c *recordingResolverClientConn) ReportError(err error) {
	c.errors = append(c.errors, err)
}

func (c *recordingResolverClientConn) NewAddress([]resolver.Address) {}

func (c *recordingResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

func (c *recordingResolverClientConn) updateCount() int {
	return len(c.states)
}

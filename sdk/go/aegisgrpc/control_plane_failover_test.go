package aegisgrpc

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/security"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
	"google.golang.org/protobuf/proto"
)

// TestRegistryResolverUsesAvailableControllerAddress locks the registry resolver uses available controller address contract so future changes do not regress it.
func TestRegistryResolverUsesAvailableControllerAddress(t *testing.T) {
	registerDefaultResolver()
	registryServer := &staticRegistryServer{
		instances:      []*aegisv1.ServiceInstance{{Id: "user-a", Service: "user-service", Address: "127.0.0.1:7001", Status: "HEALTHY"}},
		watchInstances: []*aegisv1.ServiceInstance{{Id: "user-a", Service: "user-service", Address: "127.0.0.1:7002", Status: "HEALTHY"}},
		version:        7,
		watchVersion:   8,
	}
	live := startControlPlaneTestServer(t, func(server *grpc.Server) {
		aegisv1.RegisterRegistryServiceServer(server, registryServer)
	})
	defer live.stop()

	addressesID := registerControllerAddresses([]string{unusedControllerAddr(t), live.addr})
	target := targetForServiceWithControlPlaneConfig(unusedControllerAddr(t), "user-service", "", "", addressesID)
	parsed, err := parseResolverTarget(target)
	if err != nil {
		t.Fatalf("parse resolver target: %v", err)
	}
	cc := newWaitingResolverClientConn()
	res, err := newRegistryResolverBuilder().Build(resolver.Target{URL: *parsed}, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("build registry resolver: %v", err)
	}
	defer res.Close()

	state := cc.waitForAddress(t, "127.0.0.1:7001", 3*time.Second)
	if len(state.Addresses) != 1 {
		t.Fatalf("expected one initial resolver address, got %+v", state.Addresses)
	}
	state = cc.waitForAddress(t, "127.0.0.1:7002", 3*time.Second)
	if len(state.Addresses) != 1 {
		t.Fatalf("expected one watch resolver address, got %+v", state.Addresses)
	}
	if registryServer.listCount() == 0 || registryServer.watchCount() == 0 {
		t.Fatalf("expected live controller ListInstances and WatchInstances, list=%d watch=%d", registryServer.listCount(), registryServer.watchCount())
	}
}

// TestPolicyWatcherUsesAvailableControllerAddress locks the policy watcher uses available controller address contract so future changes do not regress it.
func TestPolicyWatcherUsesAvailableControllerAddress(t *testing.T) {
	registerDefaultResolver()
	live := startControlPlaneTestServer(t, func(server *grpc.Server) {
		aegisv1.RegisterPolicyServiceServer(server, failoverPolicyServer{
			snapshot: &aegisv1.PolicySnapshot{
				Service:  "user-service",
				Revision: 11,
				CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
					MaxInflightPerEndpoint: 4,
				},
			},
		})
	})
	defer live.stop()

	addressesID := registerControllerAddresses([]string{unusedControllerAddr(t), live.addr})
	defer unregisterControllerAddresses(addressesID)
	target := controllerTargetForAddressesID(addressesID)
	dialOptions, err := controllerDialOptions(security.ClientConfig{})
	if err != nil {
		t.Fatalf("controller dial options: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &policyManager{}
	startPolicyWatcher(ctx, target, "user-service", manager, dialOptions)

	deadline := time.After(3 * time.Second)
	for {
		if policy := manager.Load(); policy != nil && policy.circuitBreaker.maxInflightPerEndpoint == 4 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("policy watcher did not receive update through live controller")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestTelemetryReporterUsesAvailableControllerAddress locks the telemetry reporter uses available controller address contract so future changes do not regress it.
func TestTelemetryReporterUsesAvailableControllerAddress(t *testing.T) {
	registerDefaultResolver()
	telemetryServer := &countingTelemetryServer{}
	live := startControlPlaneTestServer(t, func(server *grpc.Server) {
		aegisv1.RegisterTelemetryServiceServer(server, telemetryServer)
	})
	defer live.stop()

	addressesID := registerControllerAddresses([]string{unusedControllerAddr(t), live.addr})
	defer unregisterControllerAddresses(addressesID)
	target := controllerTargetForAddressesID(addressesID)
	dialOptions, err := controllerDialOptions(security.ClientConfig{})
	if err != nil {
		t.Fatalf("controller dial options: %v", err)
	}
	conn, err := grpc.DialContext(context.Background(), target, dialOptions...)
	if err != nil {
		t.Fatalf("dial control plane target: %v", err)
	}
	defer conn.Close()

	recorder := telemetry.NewRecorder("frontend", nil)
	recorder.Observe(telemetry.Observation{
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     25 * time.Millisecond,
	})
	reporter := newTelemetryReporter(aegisv1.NewTelemetryServiceClient(conn), recorder, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := reporter.ReportOnce(ctx); err != nil {
		t.Fatalf("report telemetry through live controller: %v", err)
	}
	if got := telemetryServer.sampleCount(); got != 1 {
		t.Fatalf("expected one telemetry sample through live controller, got %d", got)
	}
}

// startControlPlaneTestServer starts an in-process controller fixture and returns the address used by failover tests.
func startControlPlaneTestServer(t *testing.T, register func(*grpc.Server)) controlPlaneTestServer {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen control-plane test server: %v", err)
	}
	server := grpc.NewServer()
	register(server)
	go func() {
		_ = server.Serve(lis)
	}()
	return controlPlaneTestServer{
		addr: lis.Addr().String(),
		stop: func() {
			server.Stop()
			_ = lis.Close()
		},
	}
}

// controlPlaneTestServer carries control plane test server state for resolver, picker, and reporter state.
type controlPlaneTestServer struct {
	addr string
	stop func()
}

// unusedControllerAddr provides the shared unused controller addr helper for resolver, picker, and reporter state.
func unusedControllerAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unused controller address: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("close unused listener: %v", err)
	}
	return addr
}

// parseResolverTarget decodes resolver target input into the package's typed representation.
func parseResolverTarget(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// staticRegistryServer carries static registry server state for resolver, picker, and reporter state.
type staticRegistryServer struct {
	aegisv1.UnimplementedRegistryServiceServer
	mu             sync.Mutex
	instances      []*aegisv1.ServiceInstance
	watchInstances []*aegisv1.ServiceInstance
	version        int64
	watchVersion   int64
	lists          int
	watches        int
}

// ListInstances returns a point-in-time list of list instances visible to the caller.
func (s *staticRegistryServer) ListInstances(context.Context, *aegisv1.ListInstancesRequest) (*aegisv1.ListInstancesResponse, error) {
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	return &aegisv1.ListInstancesResponse{Instances: cloneServiceInstances(s.instances), Version: s.version}, nil
}

// WatchInstances streams instances changes to callers until the source or context closes.
func (s *staticRegistryServer) WatchInstances(_ *aegisv1.WatchInstancesRequest, stream aegisv1.RegistryService_WatchInstancesServer) error {
	s.mu.Lock()
	s.watches++
	s.mu.Unlock()
	if err := stream.Send(&aegisv1.ListInstancesResponse{Instances: cloneServiceInstances(s.watchInstances), Version: s.watchVersion}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

// listCount returns a point-in-time list of list count visible to the caller.
func (s *staticRegistryServer) listCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists
}

// watchCount streams count changes to callers until the source or context closes.
func (s *staticRegistryServer) watchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watches
}

// cloneServiceInstances returns an isolated copy of clone service instances input so callers cannot mutate shared state.
func cloneServiceInstances(instances []*aegisv1.ServiceInstance) []*aegisv1.ServiceInstance {
	out := make([]*aegisv1.ServiceInstance, 0, len(instances))
	for _, inst := range instances {
		out = append(out, proto.Clone(inst).(*aegisv1.ServiceInstance))
	}
	return out
}

// failoverPolicyServer carries failover policy server state for resolver, picker, and reporter state.
type failoverPolicyServer struct {
	aegisv1.UnimplementedPolicyServiceServer
	snapshot *aegisv1.PolicySnapshot
}

// WatchPolicy streams policy changes to callers until the source or context closes.
func (s failoverPolicyServer) WatchPolicy(req *aegisv1.WatchPolicyRequest, stream aegisv1.PolicyService_WatchPolicyServer) error {
	if req.GetService() != s.snapshot.GetService() {
		return nil
	}
	if err := stream.Send(proto.Clone(s.snapshot).(*aegisv1.PolicySnapshot)); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

// countingTelemetryServer carries counting telemetry server state for resolver, picker, and reporter state.
type countingTelemetryServer struct {
	aegisv1.UnimplementedTelemetryServiceServer
	mu      sync.Mutex
	samples int
}

// ReportEndpointStats counts received samples so failover tests can observe reporter recovery.
func (s *countingTelemetryServer) ReportEndpointStats(_ context.Context, req *aegisv1.ReportEndpointStatsRequest) (*aegisv1.ReportEndpointStatsResponse, error) {
	s.mu.Lock()
	s.samples += len(req.GetSamples())
	s.mu.Unlock()
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

// sampleCount returns sample count data for countingTelemetryServer callers without handing out mutable receiver state.
func (s *countingTelemetryServer) sampleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.samples
}

// waitingResolverClientConn carries waiting resolver client conn state for resolver, picker, and reporter state.
type waitingResolverClientConn struct {
	mu      sync.Mutex
	state   resolver.State
	history []resolver.State
	ch      chan struct{}
}

// newWaitingResolverClientConn initializes waiting resolver client conn with package defaults for this package's call path.
func newWaitingResolverClientConn() *waitingResolverClientConn {
	return &waitingResolverClientConn{ch: make(chan struct{}, 1)}
}

// UpdateState records fake controller health updates without running the production state machine.
func (c *waitingResolverClientConn) UpdateState(state resolver.State) error {
	c.mu.Lock()
	c.state = state
	c.history = append(c.history, state)
	c.mu.Unlock()
	select {
	case c.ch <- struct{}{}:
	default:
	}
	return nil
}

// waitForAddress waits for wait for address to reach the expected state or timeout.
func (c *waitingResolverClientConn) waitForAddress(t *testing.T, address string, timeout time.Duration) resolver.State {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		state, ok := resolverStateWithAddress(c.history, address)
		c.mu.Unlock()
		if ok {
			return state
		}
		select {
		case <-c.ch:
		case <-deadline:
			c.mu.Lock()
			state := c.state
			c.mu.Unlock()
			t.Fatalf("timed out waiting for resolver address %q; last=%+v", address, state)
		}
	}
}

// resolverStateWithAddress refreshes resolver state from the controller.
func resolverStateWithAddress(states []resolver.State, address string) (resolver.State, bool) {
	for _, state := range states {
		for _, addr := range state.Addresses {
			if addr.Addr == address {
				return state, true
			}
		}
	}
	return resolver.State{}, false
}

// ReportError records resolver errors while failover tests wait for an eventual ready state.
func (c *waitingResolverClientConn) ReportError(error) {}

// NewAddress initializes address with package defaults for this package's call path.
func (c *waitingResolverClientConn) NewAddress([]resolver.Address) {}

// ParseServiceConfig decodes service config input into the package's typed representation.
func (c *waitingResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

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

type controlPlaneTestServer struct {
	addr string
	stop func()
}

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

func parseResolverTarget(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

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

func (s *staticRegistryServer) ListInstances(context.Context, *aegisv1.ListInstancesRequest) (*aegisv1.ListInstancesResponse, error) {
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	return &aegisv1.ListInstancesResponse{Instances: cloneServiceInstances(s.instances), Version: s.version}, nil
}

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

func (s *staticRegistryServer) listCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists
}

func (s *staticRegistryServer) watchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watches
}

func cloneServiceInstances(instances []*aegisv1.ServiceInstance) []*aegisv1.ServiceInstance {
	out := make([]*aegisv1.ServiceInstance, 0, len(instances))
	for _, inst := range instances {
		out = append(out, proto.Clone(inst).(*aegisv1.ServiceInstance))
	}
	return out
}

type failoverPolicyServer struct {
	aegisv1.UnimplementedPolicyServiceServer
	snapshot *aegisv1.PolicySnapshot
}

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

type countingTelemetryServer struct {
	aegisv1.UnimplementedTelemetryServiceServer
	mu      sync.Mutex
	samples int
}

func (s *countingTelemetryServer) ReportEndpointStats(_ context.Context, req *aegisv1.ReportEndpointStatsRequest) (*aegisv1.ReportEndpointStatsResponse, error) {
	s.mu.Lock()
	s.samples += len(req.GetSamples())
	s.mu.Unlock()
	return &aegisv1.ReportEndpointStatsResponse{}, nil
}

func (s *countingTelemetryServer) sampleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.samples
}

type waitingResolverClientConn struct {
	mu      sync.Mutex
	state   resolver.State
	history []resolver.State
	ch      chan struct{}
}

func newWaitingResolverClientConn() *waitingResolverClientConn {
	return &waitingResolverClientConn{ch: make(chan struct{}, 1)}
}

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

func (c *waitingResolverClientConn) ReportError(error) {}

func (c *waitingResolverClientConn) NewAddress([]resolver.Address) {}

func (c *waitingResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

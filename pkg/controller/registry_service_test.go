package controller

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestRegistryServiceReturnsOwnerTokenAndRequiresFencedHeartbeat locks the registry service returns owner token and requires fenced heartbeat contract so future changes do not regress it.
func TestRegistryServiceReturnsOwnerTokenAndRequiresFencedHeartbeat(t *testing.T) {
	now := time.Date(2026, 6, 29, 14, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 30*time.Second)
	ctx := context.Background()

	registered, err := service.RegisterInstance(ctx, &aegisv1.RegisterInstanceRequest{
		Instance: &aegisv1.ServiceInstance{Id: "user-a", Service: "user-service", Address: "127.0.0.1:7001"},
	})
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}
	epoch := registered.GetInstance().GetRegistrationEpoch()
	token := registered.GetOwnerToken()
	if epoch == "" || token == "" {
		t.Fatalf("expected owner credentials in register response: %+v", registered)
	}

	_, err = service.Heartbeat(ctx, &aegisv1.HeartbeatRequest{Service: "user-service", InstanceId: "user-a"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected missing owner credentials to be invalid, got %v", err)
	}
	_, err = service.Heartbeat(ctx, &aegisv1.HeartbeatRequest{Service: "user-service", InstanceId: "user-a", RegistrationEpoch: epoch, OwnerToken: "wrong"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected stale owner to fail precondition, got %v", err)
	}
	if _, err := service.Heartbeat(ctx, &aegisv1.HeartbeatRequest{Service: "user-service", InstanceId: "user-a", RegistrationEpoch: epoch, OwnerToken: token}); err != nil {
		t.Fatalf("heartbeat with owner credentials: %v", err)
	}
}

// TestRegistryServiceRegistersAndListsInstances locks the registry service registers and lists instances contract so future changes do not regress it.
func TestRegistryServiceRegistersAndListsInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 30*time.Second)

	_, err := service.RegisterInstance(context.Background(), &aegisv1.RegisterInstanceRequest{
		Instance: &aegisv1.ServiceInstance{
			Id:      "user-1",
			Service: "user-service",
			Address: "127.0.0.1:7001",
			Status:  registry.InstanceHealthy.String(),
			Labels:  map[string]string{"variant": "primary"},
		},
		LeaseTtlSeconds: 10,
	})
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	got, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{
		Service: "user-service",
	})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(got.Instances))
	}
	if got.Instances[0].Id != "user-1" || got.Instances[0].Address != "127.0.0.1:7001" {
		t.Fatalf("unexpected instance: %+v", got.Instances[0])
	}
	if got.Instances[0].Labels["variant"] != "primary" {
		t.Fatalf("expected label variant=primary, got %+v", got.Instances[0].Labels)
	}
}

// TestRegistryServiceDirectHandlerHonorsPropagatedPrincipalScope locks the registry service direct handler honors propagated principal scope contract so future changes do not regress it.
func TestRegistryServiceDirectHandlerHonorsPropagatedPrincipalScope(t *testing.T) {
	store := registry.NewMemoryRegistry(time.Now)
	service := NewRegistryService(store, 30*time.Second)
	ctx := security.ContextWithPrincipal(context.Background(), security.Principal{Role: security.RoleSDK, Services: []string{"user-service"}})

	if _, err := service.ListInstances(ctx, &aegisv1.ListInstancesRequest{Service: "user-service"}); err != nil {
		t.Fatalf("expected scoped principal to list own service: %v", err)
	}
	_, err := service.ListInstances(ctx, &aegisv1.ListInstancesRequest{Service: "order-service"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected direct handler to reject out-of-scope service, got %v", err)
	}
}

// TestRegistryServiceUsesDefaultLeaseWhenRequestOmitsTTL locks the registry service uses default lease when request omits ttl contract so future changes do not regress it.
func TestRegistryServiceUsesDefaultLeaseWhenRequestOmitsTTL(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 5*time.Second)

	_, err := service.RegisterInstance(context.Background(), &aegisv1.RegisterInstanceRequest{
		Instance: &aegisv1.ServiceInstance{
			Id:      "order-1",
			Service: "order-service",
			Address: "127.0.0.1:7101",
		},
	})
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	now = now.Add(4 * time.Second)
	store.SweepExpired(context.Background())
	got, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "order-service"})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("expected default lease to keep instance live, got %+v", got.Instances)
	}

	now = now.Add(2 * time.Second)
	store.SweepExpired(context.Background())
	got, err = service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "order-service"})
	if err != nil {
		t.Fatalf("list instances after expiry: %v", err)
	}
	if len(got.Instances) != 0 {
		t.Fatalf("expected instance to expire after default lease, got %+v", got.Instances)
	}
}

// TestRegistryServiceListInstancesVersionTracksRegistryChanges locks the registry service list instances version tracks registry changes contract so future changes do not regress it.
func TestRegistryServiceListInstancesVersionTracksRegistryChanges(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 30*time.Second)
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")

	first, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if first.Version == 0 {
		t.Fatalf("expected non-zero list version")
	}
	second, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("unchanged list version drifted: first %d second %d", first.Version, second.Version)
	}

	now = now.Add(time.Second)
	if err := store.Heartbeat(context.Background(), "user-service", "user-a", time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	third, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("third list: %v", err)
	}
	if third.Version == first.Version {
		t.Fatalf("registry heartbeat did not change response version: %d", third.Version)
	}
}

// TestRegistryServiceListInstancesVersionTracksHealthOverlay locks the registry service list instances version tracks health overlay contract so future changes do not regress it.
func TestRegistryServiceListInstancesVersionTracksHealthOverlay(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	health := &versionedHealthProvider{}
	service := NewRegistryServiceWithHealth(store, 30*time.Second, health)
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")

	before, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("list before health: %v", err)
	}
	health.set("user-service", "user-a", fault.StateDegraded, 1.75, 1)
	after, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("list after health: %v", err)
	}
	if after.Version == before.Version {
		t.Fatalf("health revision did not change response version: %d", after.Version)
	}
	if after.Instances[0].Status != fault.StateDegraded.String() || after.Instances[0].SlowScore != 1.75 {
		t.Fatalf("expected health overlay in response, got %+v", after.Instances[0])
	}
}

// TestRegistryServiceWatchInstancesSendsInitialAndRegistryUpdates locks the registry service watch instances sends initial and registry updates contract so future changes do not regress it.
func TestRegistryServiceWatchInstancesSendsInitialAndRegistryUpdates(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newRegistryWatchTestStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.WatchInstances(&aegisv1.WatchInstancesRequest{Service: "user-service"}, stream)
	}()

	initial := stream.receive(t)
	if len(initial.Instances) != 0 || initial.Version == 0 {
		t.Fatalf("expected initial empty versioned snapshot, got %+v", initial)
	}

	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	updated := stream.receive(t)
	if updated.Version == initial.Version || len(updated.Instances) != 1 || updated.Instances[0].Id != "user-a" {
		t.Fatalf("expected watched registry update, got %+v after %+v", updated, initial)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatalf("watch did not exit after context cancellation")
	}
}

// TestRegistryServiceWatchInstancesReopensClosedRegistryWatch locks the registry service watch instances reopens closed registry watch contract so future changes do not regress it.
func TestRegistryServiceWatchInstancesReopensClosedRegistryWatch(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := &closingFirstWatchRegistry{
		MemoryRegistry: registry.NewMemoryRegistry(func() time.Time { return now }),
		firstClosed:    make(chan struct{}),
	}
	service := NewRegistryService(store, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newRegistryWatchTestStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.WatchInstances(&aegisv1.WatchInstancesRequest{Service: "user-service"}, stream)
	}()

	initial := stream.receive(t)
	if len(initial.Instances) != 0 || initial.Version == 0 {
		t.Fatalf("expected initial empty versioned snapshot, got %+v", initial)
	}
	select {
	case <-store.firstClosed:
	case <-time.After(time.Second):
		t.Fatalf("first registry watch did not close")
	}

	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	updated := stream.receiveWithin(t, 5*time.Second)
	if updated.Version == initial.Version || len(updated.Instances) != 1 || updated.Instances[0].Id != "user-a" {
		t.Fatalf("expected reopened registry watch update, got %+v after %+v", updated, initial)
	}
	if got := store.watchCalls.Load(); got < 2 {
		t.Fatalf("expected registry watch to be reopened, calls=%d", got)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatalf("watch did not exit after context cancellation")
	}
}

// closingFirstWatchRegistry forces the first watch stream to close so tests cover registry-service resubscription.
type closingFirstWatchRegistry struct {
	*registry.MemoryRegistry
	watchCalls  atomic.Int32
	firstClosed chan struct{}
}

// Watch returns a closed first stream, then delegates to the embedded registry for later watch attempts.
func (s *closingFirstWatchRegistry) Watch(ctx context.Context, service string, afterVersion int64) (<-chan registry.InstanceSnapshot, error) {
	if s.watchCalls.Add(1) == 1 {
		updates := make(chan registry.InstanceSnapshot)
		close(updates)
		close(s.firstClosed)
		return updates, nil
	}
	return s.MemoryRegistry.Watch(ctx, service, afterVersion)
}

// versionedHealthProvider carries versioned health provider state for this package call path.
type versionedHealthProvider struct {
	health  fault.EndpointHealth
	version int64
}

// set updates set state while preserving package invariants.
func (p *versionedHealthProvider) set(service, instanceID string, state fault.EndpointState, slowScore float64, version int64) {
	p.health = fault.EndpointHealth{Service: service, InstanceID: instanceID, State: state, SlowScore: slowScore}
	p.version = version
}

// HealthState returns health state data for versionedHealthProvider callers without handing out mutable receiver state.
func (p *versionedHealthProvider) HealthState(service, instanceID string) (fault.EndpointState, bool) {
	if p.health.Service != service || p.health.InstanceID != instanceID {
		return fault.StateUnspecified, false
	}
	return p.health.State, true
}

// Get returns get state for the requested key.
func (p *versionedHealthProvider) Get(service, instanceID string) (fault.EndpointHealth, bool) {
	if p.health.Service != service || p.health.InstanceID != instanceID {
		return fault.EndpointHealth{}, false
	}
	return p.health, true
}

// HealthVersion returns health version data for versionedHealthProvider callers without handing out mutable receiver state.
func (p *versionedHealthProvider) HealthVersion(service string) int64 {
	if p.health.Service != service {
		return 0
	}
	return p.version
}

// registryWatchTestStream carries registry watch test stream state for this package call path.
type registryWatchTestStream struct {
	ctx  context.Context
	sent chan *aegisv1.ListInstancesResponse
}

// newRegistryWatchTestStream initializes registry watch test stream with package defaults for this package's call path.
func newRegistryWatchTestStream(ctx context.Context) *registryWatchTestStream {
	return &registryWatchTestStream{ctx: ctx, sent: make(chan *aegisv1.ListInstancesResponse, 4)}
}

// Send enqueues registry snapshots so watch tests can assert streamed updates.
func (s *registryWatchTestStream) Send(resp *aegisv1.ListInstancesResponse) error {
	select {
	case s.sent <- resp:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// receive returns receive data for registryWatchTestStream callers without handing out mutable receiver state.
func (s *registryWatchTestStream) receive(t *testing.T) *aegisv1.ListInstancesResponse {
	t.Helper()
	return s.receiveWithin(t, time.Second)
}

// receiveWithin returns receive within data for registryWatchTestStream callers without handing out mutable receiver state.
func (s *registryWatchTestStream) receiveWithin(t *testing.T, timeout time.Duration) *aegisv1.ListInstancesResponse {
	t.Helper()
	select {
	case resp := <-s.sent:
		return resp
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for watch response")
		return nil
	}
}

// SetHeader updates set header state while preserving package invariants.
func (s *registryWatchTestStream) SetHeader(metadata.MD) error { return nil }

// SendHeader satisfies grpc.ServerStream for this fixture without emitting metadata.
func (s *registryWatchTestStream) SendHeader(metadata.MD) error { return nil }

// SetTrailer updates set trailer state while preserving package invariants.
func (s *registryWatchTestStream) SetTrailer(metadata.MD) {}

// Context exposes the fixture context used to cancel registry watch streams.
func (s *registryWatchTestStream) Context() context.Context { return s.ctx }

// SendMsg is a no-op because registry watch tests use the typed Send method.
func (s *registryWatchTestStream) SendMsg(any) error { return nil }

// RecvMsg is a no-op because server-side registry watch streams do not receive messages.
func (s *registryWatchTestStream) RecvMsg(any) error { return io.EOF }

package controller

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
)

// TestTelemetryServiceReportsStatsAndReturnsHealth locks the telemetry service reports stats and returns health contract so future changes do not regress it.
func TestTelemetryServiceReportsStatsAndReturnsHealth(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	registerTestInstance(t, store, "user-service", "user-b", "127.0.0.1:7002")
	registerTestInstance(t, store, "user-service", "user-c", "127.0.0.1:7003")

	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		Now:        func() time.Time { return now },
		LatencySLO: 100 * time.Millisecond,
		StateMachine: fault.StateMachineConfig{
			DegradedThreshold:  1.5,
			EjectThreshold:     2.5,
			ConsecutiveWindows: 1,
			EjectionDuration:   30 * time.Second,
			RecoveryThreshold:  1.0,
		},
	})
	service := NewTelemetryService(store, manager, nil)

	resp, err := service.ReportEndpointStats(context.Background(), &aegisv1.ReportEndpointStatsRequest{
		Samples: []*aegisv1.EndpointStatsSample{
			{Service: "user-service", EndpointAddress: "127.0.0.1:7001", RequestCount: 100, LatencyP95Seconds: 0.100, Capacity: 100},
			{Service: "user-service", EndpointAddress: "127.0.0.1:7002", RequestCount: 100, LatencyP95Seconds: 0.110, Capacity: 100},
			{Service: "user-service", EndpointAddress: "127.0.0.1:7003", RequestCount: 100, ErrorCount: 30, Inflight: 95, LatencyP95Seconds: 0.600, TcpRetransmit: 10, Capacity: 100},
		},
	})
	if err != nil {
		t.Fatalf("report endpoint stats: %v", err)
	}

	if len(resp.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoint health rows, got %d", len(resp.Endpoints))
	}
	slow := findHealth(resp.Endpoints, "user-c")
	if slow == nil {
		t.Fatalf("expected resolved instance id user-c in response: %+v", resp.Endpoints)
	}
	if slow.State != fault.StateEjected.String() {
		t.Fatalf("expected slow endpoint ejected, got %+v", slow)
	}
}

// TestTelemetryServiceDropsStaleRegistrationEpochSamples locks the telemetry service drops stale registration epoch samples contract so future changes do not regress it.
func TestTelemetryServiceDropsStaleRegistrationEpochSamples(t *testing.T) {
	now := time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	instances, err := store.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list registered instance: %v", err)
	}
	epoch := instances[0].RegistrationEpoch
	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		Now:        func() time.Time { return now },
		LatencySLO: 100 * time.Millisecond,
		StateMachine: fault.StateMachineConfig{
			DegradedThreshold:  0.2,
			EjectThreshold:     0.4,
			ConsecutiveWindows: 1,
		},
	})
	service := NewTelemetryService(store, manager, nil)

	stale, err := service.ReportEndpointStats(context.Background(), &aegisv1.ReportEndpointStatsRequest{Samples: []*aegisv1.EndpointStatsSample{{
		Service:           "user-service",
		InstanceId:        "user-a",
		RegistrationEpoch: "stale-epoch",
		RequestCount:      100,
		LatencyP95Seconds: 1.0,
		Capacity:          100,
	}}})
	if err != nil {
		t.Fatalf("report stale sample: %v", err)
	}
	if len(stale.Endpoints) != 0 {
		t.Fatalf("expected stale epoch sample to be dropped, got %+v", stale.Endpoints)
	}

	current, err := service.ReportEndpointStats(context.Background(), &aegisv1.ReportEndpointStatsRequest{Samples: []*aegisv1.EndpointStatsSample{{
		Service:           "user-service",
		InstanceId:        "user-a",
		RegistrationEpoch: epoch,
		RequestCount:      100,
		LatencyP95Seconds: 1.0,
		Capacity:          100,
	}}})
	if err != nil {
		t.Fatalf("report current sample: %v", err)
	}
	got := findHealth(current.Endpoints, "user-a")
	if got == nil || got.GetRegistrationEpoch() != epoch || got.State != fault.StateEjected.String() {
		t.Fatalf("expected current epoch health, got %+v", current.Endpoints)
	}
}

// TestTelemetryServicePersistsHealthSnapshot locks the telemetry service persists health snapshot contract so future changes do not regress it.
func TestTelemetryServicePersistsHealthSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	registerTestInstance(t, store, "user-service", "user-b", "127.0.0.1:7002")
	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		Now:        func() time.Time { return now },
		LatencySLO: 100 * time.Millisecond,
		StateMachine: fault.StateMachineConfig{
			DegradedThreshold:  0.2,
			EjectThreshold:     0.4,
			ConsecutiveWindows: 1,
			EjectionDuration:   time.Minute,
		},
	})
	healthStore := &recordingHealthSnapshotStore{}
	service := NewTelemetryServiceWithHealthStore(store, manager, nil, healthStore)

	_, err := service.ReportEndpointStats(context.Background(), &aegisv1.ReportEndpointStatsRequest{
		Samples: []*aegisv1.EndpointStatsSample{
			{Service: "user-service", EndpointAddress: "127.0.0.1:7002", RequestCount: 100, LatencyP95Seconds: 0.05, Capacity: 100},
			{
				Service:           "user-service",
				EndpointAddress:   "127.0.0.1:7001",
				RequestCount:      100,
				LatencyP95Seconds: 1.0,
				Capacity:          100,
			},
		},
	})
	if err != nil {
		t.Fatalf("report endpoint stats: %v", err)
	}
	var slow fault.EndpointHealth
	for _, health := range healthStore.saved {
		if health.InstanceID == "user-a" {
			slow = health
			break
		}
	}
	if slow.Service != "user-service" || slow.State != fault.StateEjected || slow.UpdatedAt.IsZero() {
		t.Fatalf("unexpected persisted slow health snapshot: %+v", slow)
	}
}

// TestTelemetryServiceResolvesDockerPeerAddressByUniquePort locks the telemetry service resolves docker peer address by unique port contract so future changes do not regress it.
func TestTelemetryServiceResolvesDockerPeerAddressByUniquePort(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-a", "user-a:7001")

	service := NewTelemetryService(store, fault.NewHealthManager(fault.HealthManagerConfig{
		Now: func() time.Time { return now },
	}), nil)

	resp, err := service.ReportEndpointStats(context.Background(), &aegisv1.ReportEndpointStatsRequest{
		Samples: []*aegisv1.EndpointStatsSample{
			{Service: "user-service", EndpointAddress: "172.20.0.4:7001", RequestCount: 10, LatencyP95Seconds: 0.050},
		},
	})
	if err != nil {
		t.Fatalf("report endpoint stats: %v", err)
	}

	if got := findHealth(resp.Endpoints, "user-a"); got == nil {
		t.Fatalf("expected Docker peer address to resolve to user-a, got %+v", resp.Endpoints)
	}
}

// TestRegistryServiceOverlaysHealthStateOnDiscoveredInstances locks the registry service overlays health state on discovered instances contract so future changes do not regress it.
func TestRegistryServiceOverlaysHealthStateOnDiscoveredInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-c", "127.0.0.1:7003")

	health := staticHealthProvider{state: fault.StateEjected}
	service := NewRegistryServiceWithHealth(store, 30*time.Second, health)

	resp, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(resp.Instances) != 1 {
		t.Fatalf("expected one instance, got %d", len(resp.Instances))
	}
	if resp.Instances[0].Status != fault.StateEjected.String() {
		t.Fatalf("expected discovered instance status to be overlaid as EJECTED, got %+v", resp.Instances[0])
	}
}

// TestRegistryServiceSkipsHealthOverlayWhenAddressMismatch locks the registry service skips health overlay when address mismatch contract so future changes do not regress it.
func TestRegistryServiceSkipsHealthOverlayWhenAddressMismatch(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-c", "127.0.0.1:7003")

	health := staticHealthProvider{state: fault.StateEjected, slowScore: 1.75, address: "127.0.0.1:7103"}
	service := NewRegistryServiceWithHealth(store, 30*time.Second, health)

	resp, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(resp.Instances) != 1 {
		t.Fatalf("expected one instance, got %d", len(resp.Instances))
	}
	if resp.Instances[0].Status != registry.InstanceHealthy.String() || resp.Instances[0].SlowScore != 0 {
		t.Fatalf("expected address-mismatched health overlay to be skipped, got %+v", resp.Instances[0])
	}
}

// TestRegistryServiceOverlaysSlowScoreOnDiscoveredInstances locks the registry service overlays slow score on discovered instances contract so future changes do not regress it.
func TestRegistryServiceOverlaysSlowScoreOnDiscoveredInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-c", "127.0.0.1:7003")

	health := staticHealthProvider{state: fault.StateDegraded, slowScore: 1.75}
	service := NewRegistryServiceWithHealth(store, 30*time.Second, health)

	resp, err := service.ListInstances(context.Background(), &aegisv1.ListInstancesRequest{Service: "user-service"})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if resp.Instances[0].SlowScore != 1.75 {
		t.Fatalf("expected slow_score overlay 1.75, got %+v", resp.Instances[0])
	}
}

// registerTestInstance registers register test instance with the controller or local registry.
func registerTestInstance(t *testing.T, store registry.Registry, service, id, address string) {
	t.Helper()
	if err := store.Register(context.Background(), registry.Instance{
		ID:      id,
		Service: service,
		Address: address,
		Status:  registry.InstanceHealthy,
	}, time.Minute); err != nil {
		t.Fatalf("register %s/%s: %v", service, id, err)
	}
}

// findHealth provides the shared find health helper for this package call path.
func findHealth(endpoints []*aegisv1.EndpointHealth, instanceID string) *aegisv1.EndpointHealth {
	for _, endpoint := range endpoints {
		if endpoint.InstanceId == instanceID {
			return endpoint
		}
	}
	return nil
}

// staticHealthProvider carries static health provider state for this package call path.
type staticHealthProvider struct {
	state     fault.EndpointState
	slowScore float64
	address   string
}

// HealthState returns health state data for staticHealthProvider callers without handing out mutable receiver state.
func (p staticHealthProvider) HealthState(service, instanceID string) (fault.EndpointState, bool) {
	return p.state, true
}

// Get returns get state for the requested key.
func (p staticHealthProvider) Get(service, instanceID string) (fault.EndpointHealth, bool) {
	return fault.EndpointHealth{
		Service:    service,
		InstanceID: instanceID,
		State:      p.state,
		SlowScore:  p.slowScore,
		Address:    p.address,
	}, true
}

// recordingHealthSnapshotStore defines persistence operations for recording health snapshot store state.
type recordingHealthSnapshotStore struct {
	saved []fault.EndpointHealth
}

// Load reads the current state from the configured backing source.
func (s *recordingHealthSnapshotStore) Load(context.Context) ([]fault.EndpointHealth, int64, error) {
	return append([]fault.EndpointHealth(nil), s.saved...), 0, nil
}

// Save persists save state to the backing store.
func (s *recordingHealthSnapshotStore) Save(_ context.Context, health []fault.EndpointHealth) (int64, error) {
	s.saved = append([]fault.EndpointHealth(nil), health...)
	return 1, nil
}

// Watch streams backing-source changes to callers until the source or context closes.
func (s *recordingHealthSnapshotStore) Watch(ctx context.Context, _ int64) (<-chan fault.HealthStoreEvent, error) {
	ch := make(chan fault.HealthStoreEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// Close closes owned resources and makes repeated calls safe.
func (s *recordingHealthSnapshotStore) Close() error { return nil }

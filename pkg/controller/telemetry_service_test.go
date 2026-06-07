package controller

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
)

func TestTelemetryServiceReportsStatsAndReturnsHealth(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	registerTestInstance(t, store, "user-service", "user-a", "127.0.0.1:7001")
	registerTestInstance(t, store, "user-service", "user-b", "127.0.0.1:7002")
	registerTestInstance(t, store, "user-service", "user-c", "127.0.0.1:7003")

	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		Now: func() time.Time { return now },
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
	if slow.State != string(fault.StateEjected) {
		t.Fatalf("expected slow endpoint ejected, got %+v", slow)
	}
}

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
	if resp.Instances[0].Status != string(fault.StateEjected) {
		t.Fatalf("expected discovered instance status to be overlaid as EJECTED, got %+v", resp.Instances[0])
	}
}

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

func findHealth(endpoints []*aegisv1.EndpointHealth, instanceID string) *aegisv1.EndpointHealth {
	for _, endpoint := range endpoints {
		if endpoint.InstanceId == instanceID {
			return endpoint
		}
	}
	return nil
}

type staticHealthProvider struct {
	state     fault.EndpointState
	slowScore float64
}

func (p staticHealthProvider) HealthState(service, instanceID string) (fault.EndpointState, bool) {
	return p.state, true
}

func (p staticHealthProvider) Get(service, instanceID string) (fault.EndpointHealth, bool) {
	return fault.EndpointHealth{
		Service:    service,
		InstanceID: instanceID,
		State:      p.state,
		SlowScore:  p.slowScore,
	}, true
}

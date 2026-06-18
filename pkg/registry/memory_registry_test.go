package registry

import (
	"context"
	"testing"
	"time"
)

func TestMemoryRegistryRegistersAndListsLiveInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
		Labels:  map[string]string{"variant": "primary"},
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 live instance, got %d", len(got))
	}
	if got[0].ID != "user-1" || got[0].Address != "127.0.0.1:7001" {
		t.Fatalf("unexpected instance: %+v", got[0])
	}
	if got[0].Labels["variant"] != "primary" {
		t.Fatalf("expected labels to round-trip, got %+v", got[0].Labels)
	}
}

func TestMemoryRegistryExpiresInstancesAfterLease(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	now = now.Add(6 * time.Second)
	expired := reg.SweepExpired(context.Background())
	if expired != 1 {
		t.Fatalf("expected 1 expired instance, got %d", expired)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no live instances after lease expiry, got %+v", got)
	}
}

func TestMemoryRegistryHeartbeatExtendsLease(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	now = now.Add(4 * time.Second)
	if err := reg.Heartbeat(context.Background(), "user-service", "user-1", 5*time.Second); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	now = now.Add(3 * time.Second)
	expired := reg.SweepExpired(context.Background())
	if expired != 0 {
		t.Fatalf("expected heartbeat to keep instance alive, expired %d", expired)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected renewed instance to remain live, got %+v", got)
	}
}

package controller

import (
	"context"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/registry"
)

func TestRegistryServiceRegistersAndListsInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	store := registry.NewMemoryRegistry(func() time.Time { return now })
	service := NewRegistryService(store, 30*time.Second)

	_, err := service.RegisterInstance(context.Background(), &aegisv1.RegisterInstanceRequest{
		Instance: &aegisv1.ServiceInstance{
			Id:      "user-1",
			Service: "user-service",
			Address: "127.0.0.1:7001",
			Status:  string(registry.InstanceHealthy),
			Labels:  map[string]string{"version": "v1"},
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
	if got.Instances[0].Labels["version"] != "v1" {
		t.Fatalf("expected label version=v1, got %+v", got.Instances[0].Labels)
	}
}

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

package registry

import (
	"context"
	"testing"
	"time"
)

func TestFileRegistryPersistsInstancesAcrossRestart(t *testing.T) {
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/registry.json"

	first, err := NewFileRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	if err := first.Register(context.Background(), Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "user-a:7001",
		Labels:  map[string]string{"variant": "primary"},
	}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}

	second, err := NewFileRegistry(path, func() time.Time { return now.Add(10 * time.Second) })
	if err != nil {
		t.Fatalf("reload file registry: %v", err)
	}
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected one restored instance, got %+v", instances)
	}
	if instances[0].ID != "user-a" || instances[0].Address != "user-a:7001" || instances[0].Labels["variant"] != "primary" {
		t.Fatalf("unexpected restored instance: %+v", instances[0])
	}
}

func TestFileRegistryDoesNotRestoreExpiredInstances(t *testing.T) {
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/registry.json"

	first, err := NewFileRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	if err := first.Register(context.Background(), Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "user-a:7001",
	}, time.Second); err != nil {
		t.Fatalf("register instance: %v", err)
	}

	second, err := NewFileRegistry(path, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatalf("reload file registry: %v", err)
	}
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected expired instance to stay hidden, got %+v", instances)
	}
}

package routing

import (
	"context"
	"errors"
	"testing"
)

func TestRoundRobinPickerReturnsHealthyEndpointsInOrder(t *testing.T) {
	picker := NewRoundRobinPicker([]Endpoint{
		{ID: "user-a", Address: "127.0.0.1:7001", Status: EndpointHealthy},
		{ID: "user-b", Address: "127.0.0.1:7002", Status: EndpointHealthy},
	})

	want := []string{"user-a", "user-b", "user-a", "user-b"}
	for i, id := range want {
		got, err := picker.Pick(context.Background())
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.ID != id {
			t.Fatalf("pick %d: expected %s, got %s", i, id, got.ID)
		}
	}
}

func TestRoundRobinPickerSkipsUnavailableEndpoints(t *testing.T) {
	picker := NewRoundRobinPicker([]Endpoint{
		{ID: "user-a", Address: "127.0.0.1:7001", Status: EndpointUnavailable},
		{ID: "user-b", Address: "127.0.0.1:7002", Status: EndpointHealthy},
	})

	for i := 0; i < 3; i++ {
		got, err := picker.Pick(context.Background())
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.ID != "user-b" {
			t.Fatalf("expected only healthy endpoint user-b, got %s", got.ID)
		}
	}
}

func TestRoundRobinPickerReportsNoEndpointWhenAllUnavailable(t *testing.T) {
	picker := NewRoundRobinPicker([]Endpoint{
		{ID: "user-a", Address: "127.0.0.1:7001", Status: EndpointUnavailable},
	})

	_, err := picker.Pick(context.Background())
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("expected ErrNoEndpoint, got %v", err)
	}
}

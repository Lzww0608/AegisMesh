package services

import (
	"context"
	"testing"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
)

// TestOrderServerCreatesOrderWithRequestItems locks the order server creates order with request items contract so future changes do not regress it.
func TestOrderServerCreatesOrderWithRequestItems(t *testing.T) {
	server := NewOrderServer("secondary")

	got, err := server.CreateOrder(context.Background(), &shopv1.CreateOrderRequest{
		UserId:  "u-100",
		ItemIds: []string{"sku-1", "sku-2"},
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if got.UserId != "u-100" {
		t.Fatalf("expected user id u-100, got %s", got.UserId)
	}
	if got.Status != "CREATED" {
		t.Fatalf("expected CREATED status, got %s", got.Status)
	}
	if got.Variant != "secondary" {
		t.Fatalf("expected variant secondary, got %s", got.Variant)
	}
	if len(got.ItemIds) != 2 || got.ItemIds[0] != "sku-1" || got.ItemIds[1] != "sku-2" {
		t.Fatalf("unexpected item ids: %+v", got.ItemIds)
	}
	if got.OrderId == "" {
		t.Fatalf("expected order id to be set")
	}
}

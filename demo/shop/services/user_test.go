package services

import (
	"context"
	"testing"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
)

// TestUserServerReturnsUserAndVariant locks the user server returns user and variant contract so future changes do not regress it.
func TestUserServerReturnsUserAndVariant(t *testing.T) {
	server := NewUserServer("primary")

	got, err := server.GetUser(context.Background(), &shopv1.GetUserRequest{UserId: "u-100"})
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.UserId != "u-100" {
		t.Fatalf("expected user id u-100, got %s", got.UserId)
	}
	if got.Name != "user-u-100" {
		t.Fatalf("expected generated user name, got %s", got.Name)
	}
	if got.Variant != "primary" {
		t.Fatalf("expected variant primary, got %s", got.Variant)
	}
}

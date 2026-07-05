package services

import (
	"context"
	"fmt"
	"sync/atomic"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OrderServer carries order server state for this package call path.
type OrderServer struct {
	shopv1.UnimplementedOrderServiceServer

	variant string
	fault   FaultProfile
	nextID  atomic.Uint64
}

// NewOrderServer initializes order server with package defaults for this package's call path.
func NewOrderServer(variant string) *OrderServer {
	return NewOrderServerWithFault(variant, FaultProfile{})
}

// NewOrderServerWithFault initializes order server with fault with package defaults for this package's call path.
func NewOrderServerWithFault(variant string, fault FaultProfile) *OrderServer {
	if variant == "" {
		variant = "primary"
	}
	return &OrderServer{variant: variant, fault: fault}
}

// CreateOrder applies the configured fault hook, assigns a monotonic demo order ID, and copies item IDs into the response.
func (s *OrderServer) CreateOrder(ctx context.Context, req *shopv1.CreateOrderRequest) (*shopv1.CreateOrderResponse, error) {
	if err := s.fault.BeforeCall(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	id := s.nextID.Add(1)
	return &shopv1.CreateOrderResponse{
		OrderId: fmt.Sprintf("order-%06d", id),
		UserId:  req.UserId,
		ItemIds: append([]string(nil), req.ItemIds...),
		Status:  "CREATED",
		Variant: s.variant,
	}, nil
}

package services

import (
	"context"
	"fmt"
	"sync/atomic"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type OrderServer struct {
	shopv1.UnimplementedOrderServiceServer

	variant string
	fault   FaultProfile
	nextID  atomic.Uint64
}

func NewOrderServer(variant string) *OrderServer {
	return NewOrderServerWithFault(variant, FaultProfile{})
}

func NewOrderServerWithFault(variant string, fault FaultProfile) *OrderServer {
	if variant == "" {
		variant = "primary"
	}
	return &OrderServer{variant: variant, fault: fault}
}

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

package services

import (
	"context"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UserServer carries user server state for this package call path.
type UserServer struct {
	shopv1.UnimplementedUserServiceServer

	variant string
	fault   FaultProfile
}

// NewUserServer initializes user server with package defaults for this package's call path.
func NewUserServer(variant string) *UserServer {
	return NewUserServerWithFault(variant, FaultProfile{})
}

// NewUserServerWithFault initializes user server with fault with package defaults for this package's call path.
func NewUserServerWithFault(variant string, fault FaultProfile) *UserServer {
	if variant == "" {
		variant = "primary"
	}
	return &UserServer{variant: variant, fault: fault}
}

// GetUser returns get user state for the requested key.
func (s *UserServer) GetUser(ctx context.Context, req *shopv1.GetUserRequest) (*shopv1.GetUserResponse, error) {
	if err := s.fault.BeforeCall(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &shopv1.GetUserResponse{
		UserId:  req.UserId,
		Name:    "user-" + req.UserId,
		Variant: s.variant,
	}, nil
}

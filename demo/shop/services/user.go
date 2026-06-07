package services

import (
	"context"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UserServer struct {
	shopv1.UnimplementedUserServiceServer

	version string
	fault   FaultProfile
}

func NewUserServer(version string) *UserServer {
	return NewUserServerWithFault(version, FaultProfile{})
}

func NewUserServerWithFault(version string, fault FaultProfile) *UserServer {
	if version == "" {
		version = "v1"
	}
	return &UserServer{version: version, fault: fault}
}

func (s *UserServer) GetUser(ctx context.Context, req *shopv1.GetUserRequest) (*shopv1.GetUserResponse, error) {
	if err := s.fault.BeforeCall(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &shopv1.GetUserResponse{
		UserId:  req.UserId,
		Name:    "user-" + req.UserId,
		Version: s.version,
	}, nil
}

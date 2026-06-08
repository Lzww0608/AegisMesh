package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type PolicyStore interface {
	Get(service string) (*aegisv1.PolicySnapshot, bool)
}

type policyReloader interface {
	ReloadIfChanged() error
}

type PolicyService struct {
	aegisv1.UnimplementedPolicyServiceServer

	store          PolicyStore
	reloadInterval time.Duration
}

func NewPolicyService(store PolicyStore, reloadInterval time.Duration) *PolicyService {
	if reloadInterval <= 0 {
		reloadInterval = 3 * time.Second
	}
	return &PolicyService{store: store, reloadInterval: reloadInterval}
}

func (s *PolicyService) GetPolicy(_ context.Context, req *aegisv1.GetPolicyRequest) (*aegisv1.PolicySnapshot, error) {
	if req == nil || req.Service == "" {
		return nil, status.Error(codes.InvalidArgument, "service is required")
	}
	if s == nil || s.store == nil {
		return nil, status.Error(codes.NotFound, "policy store is not configured")
	}
	snapshot, ok := s.store.Get(req.Service)
	if !ok {
		return nil, status.Error(codes.NotFound, "policy not found")
	}
	return proto.Clone(snapshot).(*aegisv1.PolicySnapshot), nil
}

func (s *PolicyService) WatchPolicy(req *aegisv1.WatchPolicyRequest, stream aegisv1.PolicyService_WatchPolicyServer) error {
	if req == nil || req.Service == "" {
		return status.Error(codes.InvalidArgument, "service is required")
	}
	if s == nil || s.store == nil {
		return status.Error(codes.NotFound, "policy store is not configured")
	}

	var lastVersion int64
	if err := s.sendIfChanged(req.Service, &lastVersion, stream); err != nil {
		return err
	}

	ticker := time.NewTicker(s.reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
			if reloader, ok := s.store.(policyReloader); ok {
				if err := reloader.ReloadIfChanged(); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
			}
			if err := s.sendIfChanged(req.Service, &lastVersion, stream); err != nil {
				return err
			}
		}
	}
}

func (s *PolicyService) sendIfChanged(service string, lastVersion *int64, stream aegisv1.PolicyService_WatchPolicyServer) error {
	snapshot, ok := s.store.Get(service)
	if !ok {
		return status.Error(codes.NotFound, "policy not found")
	}
	if snapshot.Version == *lastVersion {
		return nil
	}
	*lastVersion = snapshot.Version
	return stream.Send(proto.Clone(snapshot).(*aegisv1.PolicySnapshot))
}

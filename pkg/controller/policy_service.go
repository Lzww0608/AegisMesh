package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// PolicyStore defines persistence operations for policy store state.
type PolicyStore interface {
	Get(service string) (*aegisv1.PolicySnapshot, bool)
}

// policyReloader defines the policy reloader contract used by this package call path.
type policyReloader interface {
	ReloadIfChanged() error
}

// PolicyService implements the controller RPC surface for policy service.
type PolicyService struct {
	aegisv1.UnimplementedPolicyServiceServer

	store          PolicyStore
	reloadInterval time.Duration
}

// NewPolicyService initializes policy service with package defaults for this package's call path.
func NewPolicyService(store PolicyStore, reloadInterval time.Duration) *PolicyService {
	if reloadInterval <= 0 {
		reloadInterval = 3 * time.Second
	}
	return &PolicyService{store: store, reloadInterval: reloadInterval}
}

// GetPolicy returns get policy state for the requested key.
func (s *PolicyService) GetPolicy(ctx context.Context, req *aegisv1.GetPolicyRequest) (*aegisv1.PolicySnapshot, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.PolicyService_GetPolicy_FullMethodName, req); err != nil {
		return nil, err
	}
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

// WatchPolicy streams policy changes to callers until the source or context closes.
func (s *PolicyService) WatchPolicy(req *aegisv1.WatchPolicyRequest, stream aegisv1.PolicyService_WatchPolicyServer) error {
	if err := security.AuthorizeControllerPrincipal(stream.Context(), aegisv1.PolicyService_WatchPolicy_FullMethodName, req); err != nil {
		return err
	}
	if req == nil || req.Service == "" {
		return status.Error(codes.InvalidArgument, "service is required")
	}
	if s == nil || s.store == nil {
		return status.Error(codes.NotFound, "policy store is not configured")
	}

	var lastRevision int64
	if err := s.sendIfChanged(req.Service, &lastRevision, stream); err != nil {
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
			if err := s.sendIfChanged(req.Service, &lastRevision, stream); err != nil {
				return err
			}
		}
	}
}

const deletedPolicyRevision int64 = -1

// sendIfChanged returns send if changed data for PolicyService callers without handing out mutable receiver state.
func (s *PolicyService) sendIfChanged(service string, lastRevision *int64, stream aegisv1.PolicyService_WatchPolicyServer) error {
	snapshot, ok := s.store.Get(service)
	if !ok {
		if *lastRevision == 0 {
			return status.Error(codes.NotFound, "policy not found")
		}
		if *lastRevision == deletedPolicyRevision {
			return nil
		}
		*lastRevision = deletedPolicyRevision
		return stream.Send(&aegisv1.PolicySnapshot{Service: service, Revision: deletedPolicyRevision})
	}
	if snapshot.Revision == *lastRevision {
		return nil
	}
	*lastRevision = snapshot.Revision
	return stream.Send(proto.Clone(snapshot).(*aegisv1.PolicySnapshot))
}

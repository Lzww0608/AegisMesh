package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type HealthStateProvider interface {
	HealthState(service, instanceID string) (fault.EndpointState, bool)
}

type healthSnapshotProvider interface {
	Get(service, instanceID string) (fault.EndpointHealth, bool)
}

type RegistryService struct {
	aegisv1.UnimplementedRegistryServiceServer

	store        registry.Registry
	defaultLease time.Duration
	health       HealthStateProvider
}

func NewRegistryService(store registry.Registry, defaultLease time.Duration) *RegistryService {
	return NewRegistryServiceWithHealth(store, defaultLease, nil)
}

func NewRegistryServiceWithHealth(store registry.Registry, defaultLease time.Duration, health HealthStateProvider) *RegistryService {
	if defaultLease <= 0 {
		defaultLease = 30 * time.Second
	}
	return &RegistryService{
		store:        store,
		defaultLease: defaultLease,
		health:       health,
	}
}

func (s *RegistryService) RegisterInstance(ctx context.Context, req *aegisv1.RegisterInstanceRequest) (*aegisv1.RegisterInstanceResponse, error) {
	if req == nil || req.Instance == nil {
		return nil, status.Error(codes.InvalidArgument, "instance is required")
	}

	lease := s.leaseTTL(req.LeaseTtlSeconds)
	inst := instanceFromProto(req.Instance)
	if err := s.store.Register(ctx, inst, lease); err != nil {
		return nil, statusFromRegistryError(err)
	}

	instances, err := s.store.List(ctx, inst.Service)
	if err != nil {
		return nil, statusFromRegistryError(err)
	}
	for _, registered := range instances {
		if registered.ID == inst.ID {
			return &aegisv1.RegisterInstanceResponse{Instance: instanceToProto(registered)}, nil
		}
	}
	return nil, status.Error(codes.Internal, "registered instance was not visible")
}

func (s *RegistryService) Heartbeat(ctx context.Context, req *aegisv1.HeartbeatRequest) (*aegisv1.HeartbeatResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "heartbeat request is required")
	}

	lease := s.leaseTTL(req.LeaseTtlSeconds)
	if err := s.store.Heartbeat(ctx, req.Service, req.InstanceId, lease); err != nil {
		return nil, statusFromRegistryError(err)
	}

	instances, err := s.store.List(ctx, req.Service)
	if err != nil {
		return nil, statusFromRegistryError(err)
	}
	for _, inst := range instances {
		if inst.ID == req.InstanceId {
			return &aegisv1.HeartbeatResponse{Instance: instanceToProto(inst)}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "instance not found")
}

func (s *RegistryService) ListInstances(ctx context.Context, req *aegisv1.ListInstancesRequest) (*aegisv1.ListInstancesResponse, error) {
	if req == nil || req.Service == "" {
		return nil, status.Error(codes.InvalidArgument, "service is required")
	}

	instances, err := s.store.List(ctx, req.Service)
	if err != nil {
		return nil, statusFromRegistryError(err)
	}

	out := make([]*aegisv1.ServiceInstance, 0, len(instances))
	for _, inst := range instances {
		slowScore := 0.0
		if s.health != nil {
			if state, ok := s.health.HealthState(inst.Service, inst.ID); ok {
				inst.Status = registry.InstanceStatus(state)
			}
			if provider, ok := s.health.(healthSnapshotProvider); ok {
				if health, ok := provider.Get(inst.Service, inst.ID); ok {
					inst.Status = registry.InstanceStatus(health.State)
					slowScore = health.SlowScore
				}
			}
		}
		protoInst := instanceToProto(inst)
		protoInst.SlowScore = slowScore
		out = append(out, protoInst)
	}
	return &aegisv1.ListInstancesResponse{Instances: out}, nil
}

func (s *RegistryService) leaseTTL(seconds int64) time.Duration {
	if seconds <= 0 {
		return s.defaultLease
	}
	return time.Duration(seconds) * time.Second
}

func instanceFromProto(inst *aegisv1.ServiceInstance) registry.Instance {
	statusValue := registry.InstanceStatus(inst.Status)
	if statusValue == "" {
		statusValue = registry.InstanceHealthy
	}
	return registry.Instance{
		ID:      inst.Id,
		Service: inst.Service,
		Address: inst.Address,
		Status:  statusValue,
		Labels:  cloneProtoLabels(inst.Labels),
	}
}

func instanceToProto(inst registry.Instance) *aegisv1.ServiceInstance {
	return &aegisv1.ServiceInstance{
		Id:                 inst.ID,
		Service:            inst.Service,
		Address:            inst.Address,
		Status:             string(inst.Status),
		Labels:             cloneProtoLabels(inst.Labels),
		LastSeenUnixMillis: inst.LastSeen.UnixMilli(),
	}
}

func cloneProtoLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func statusFromRegistryError(err error) error {
	switch err {
	case nil:
		return nil
	case registry.ErrInvalidInstance:
		return status.Error(codes.InvalidArgument, err.Error())
	case registry.ErrInstanceNotFound:
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

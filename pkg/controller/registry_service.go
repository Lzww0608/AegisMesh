package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const registryWatchFallbackInterval = 3 * time.Second

type HealthStateProvider interface {
	HealthState(service, instanceID string) (fault.EndpointState, bool)
}

type healthSnapshotProvider interface {
	Get(service, instanceID string) (fault.EndpointHealth, bool)
}

type healthVersionProvider interface {
	HealthVersion(service string) int64
}

type healthWatchProvider interface {
	WatchHealth(ctx context.Context, service string, afterVersion int64) (<-chan int64, error)
}

type RegistryService struct {
	aegisv1.UnimplementedRegistryServiceServer

	store        registry.Registry
	defaultLease time.Duration
	health       HealthStateProvider
}

type instancesView struct {
	response        *aegisv1.ListInstancesResponse
	registryVersion int64
	healthVersion   int64
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

	view, err := s.instancesView(ctx, req.Service)
	if err != nil {
		return nil, err
	}
	return view.response, nil
}

func (s *RegistryService) WatchInstances(req *aegisv1.WatchInstancesRequest, stream aegisv1.RegistryService_WatchInstancesServer) error {
	if req == nil || req.Service == "" {
		return status.Error(codes.InvalidArgument, "service is required")
	}

	ctx := stream.Context()
	lastSentVersion := req.LastSeenVersion
	view, err := s.sendInstancesIfChanged(ctx, req.Service, &lastSentVersion, stream)
	if err != nil {
		return err
	}

	registryUpdates := s.watchRegistry(ctx, req.Service, view.registryVersion)
	healthUpdates := s.watchHealth(ctx, req.Service, view.healthVersion)
	var ticker *time.Ticker
	var ticks <-chan time.Time
	ensureTicker := func() {
		if ticker != nil {
			return
		}
		ticker = time.NewTicker(registryWatchFallbackInterval)
		ticks = ticker.C
	}
	if registryUpdates == nil || (s.health != nil && healthUpdates == nil) {
		ensureTicker()
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-registryUpdates:
			if !ok {
				registryUpdates = nil
				ensureTicker()
				continue
			}
			view, err = s.sendInstancesIfChanged(ctx, req.Service, &lastSentVersion, stream)
			if err != nil {
				return err
			}
		case _, ok := <-healthUpdates:
			if !ok {
				healthUpdates = nil
				ensureTicker()
				continue
			}
			view, err = s.sendInstancesIfChanged(ctx, req.Service, &lastSentVersion, stream)
			if err != nil {
				return err
			}
		case <-ticks:
			view, err = s.sendInstancesIfChanged(ctx, req.Service, &lastSentVersion, stream)
			if err != nil {
				return err
			}
		}
		if registryUpdates == nil && (s.health == nil || healthUpdates == nil) {
			ensureTicker()
		}
	}
}

func (s *RegistryService) sendInstancesIfChanged(ctx context.Context, service string, lastSentVersion *int64, stream aegisv1.RegistryService_WatchInstancesServer) (instancesView, error) {
	view, err := s.instancesView(ctx, service)
	if err != nil {
		return instancesView{}, err
	}
	if view.response.Version == *lastSentVersion {
		return view, nil
	}
	if err := stream.Send(view.response); err != nil {
		return instancesView{}, err
	}
	*lastSentVersion = view.response.Version
	return view, nil
}

func (s *RegistryService) instancesView(ctx context.Context, service string) (instancesView, error) {
	snapshot, err := s.registrySnapshot(ctx, service)
	if err != nil {
		return instancesView{}, statusFromRegistryError(err)
	}
	healthVersion := s.healthVersion(service)

	out := make([]*aegisv1.ServiceInstance, 0, len(snapshot.Instances))
	for _, inst := range snapshot.Instances {
		inst, slowScore := s.overlayHealth(inst)
		protoInst := instanceToProto(inst)
		protoInst.SlowScore = slowScore
		out = append(out, protoInst)
	}
	return instancesView{
		response: &aegisv1.ListInstancesResponse{
			Instances: out,
			Version:   combineInstancesVersion(snapshot.Version, healthVersion),
		},
		registryVersion: snapshot.Version,
		healthVersion:   healthVersion,
	}, nil
}

func (s *RegistryService) registrySnapshot(ctx context.Context, service string) (registry.InstanceSnapshot, error) {
	if snapshotter, ok := s.store.(registry.Snapshotter); ok {
		return snapshotter.Snapshot(ctx, service)
	}
	instances, err := s.store.List(ctx, service)
	if err != nil {
		return registry.InstanceSnapshot{}, err
	}
	return registry.InstanceSnapshot{Service: service, Instances: instances}, nil
}

func (s *RegistryService) overlayHealth(inst registry.Instance) (registry.Instance, float64) {
	slowScore := 0.0
	if s.health == nil {
		return inst, slowScore
	}
	if state, ok := s.health.HealthState(inst.Service, inst.ID); ok {
		inst.Status = state
	}
	if provider, ok := s.health.(healthSnapshotProvider); ok {
		if health, ok := provider.Get(inst.Service, inst.ID); ok {
			inst.Status = health.State
			slowScore = health.SlowScore
		}
	}
	return inst, slowScore
}

func (s *RegistryService) watchRegistry(ctx context.Context, service string, afterVersion int64) <-chan registry.InstanceSnapshot {
	watcher, ok := s.store.(registry.Watcher)
	if !ok {
		return nil
	}
	updates, err := watcher.Watch(ctx, service, afterVersion)
	if err != nil {
		return nil
	}
	return updates
}

func (s *RegistryService) watchHealth(ctx context.Context, service string, afterVersion int64) <-chan int64 {
	watcher, ok := s.health.(healthWatchProvider)
	if !ok {
		return nil
	}
	updates, err := watcher.WatchHealth(ctx, service, afterVersion)
	if err != nil {
		return nil
	}
	return updates
}

func (s *RegistryService) healthVersion(service string) int64 {
	provider, ok := s.health.(healthVersionProvider)
	if !ok {
		return 0
	}
	return provider.HealthVersion(service)
}

func combineInstancesVersion(registryVersion, healthVersion int64) int64 {
	version := fnv64Mix(1469598103934665603, uint64(registryVersion))
	version = fnv64Mix(version, uint64(healthVersion))
	version &= 1<<63 - 1
	if version == 0 {
		return 1
	}
	return int64(version)
}

func fnv64Mix(hash, value uint64) uint64 {
	const prime uint64 = 1099511628211
	for i := 0; i < 8; i++ {
		hash ^= value & 0xff
		hash *= prime
		value >>= 8
	}
	return hash
}

func (s *RegistryService) leaseTTL(seconds int64) time.Duration {
	if seconds <= 0 {
		return s.defaultLease
	}
	return time.Duration(seconds) * time.Second
}

func instanceFromProto(inst *aegisv1.ServiceInstance) registry.Instance {
	statusValue := aegisstatus.Parse(inst.Status)
	if statusValue == aegisstatus.Unspecified {
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
		Status:             inst.Status.String(),
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

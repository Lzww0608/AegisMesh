package controller

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/aegismesh/aegismesh/pkg/security"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const registryWatchFallbackInterval = 3 * time.Second

// HealthStateProvider defines the health state provider contract used by this package call path.
type HealthStateProvider interface {
	HealthState(service, instanceID string) (fault.EndpointState, bool)
}

// healthSnapshotProvider defines the health snapshot provider contract used by this package call path.
type healthSnapshotProvider interface {
	Get(service, instanceID string) (fault.EndpointHealth, bool)
}

// healthVersionProvider defines the health version provider contract used by this package call path.
type healthVersionProvider interface {
	HealthVersion(service string) int64
}

// healthWatchProvider defines the health watch provider contract used by this package call path.
type healthWatchProvider interface {
	WatchHealth(ctx context.Context, service string, afterVersion int64) (<-chan int64, error)
}

// RegistryService implements the controller RPC surface for registry service.
type RegistryService struct {
	aegisv1.UnimplementedRegistryServiceServer

	store        registry.Registry
	defaultLease time.Duration
	health       HealthStateProvider
}

// instancesView carries instances view state for this package call path.
type instancesView struct {
	response        *aegisv1.ListInstancesResponse
	registryVersion int64
	healthVersion   int64
}

// NewRegistryService initializes registry service with package defaults for this package's call path.
func NewRegistryService(store registry.Registry, defaultLease time.Duration) *RegistryService {
	return NewRegistryServiceWithHealth(store, defaultLease, nil)
}

// NewRegistryServiceWithHealth builds a registry service and optional health overlay.
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

// RegisterInstance creates a fenced service lease and returns the owner token.
func (s *RegistryService) RegisterInstance(ctx context.Context, req *aegisv1.RegisterInstanceRequest) (*aegisv1.RegisterInstanceResponse, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_RegisterInstance_FullMethodName, req); err != nil {
		return nil, err
	}
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
			return &aegisv1.RegisterInstanceResponse{Instance: instanceToProto(registered), OwnerToken: registered.OwnerToken}, nil
		}
	}
	return nil, status.Error(codes.Internal, "registered instance was not visible")
}

// Heartbeat refreshes the instance lease using the current registration fence.
func (s *RegistryService) Heartbeat(ctx context.Context, req *aegisv1.HeartbeatRequest) (*aegisv1.HeartbeatResponse, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_Heartbeat_FullMethodName, req); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "heartbeat request is required")
	}
	if req.RegistrationEpoch == "" || req.OwnerToken == "" {
		return nil, status.Error(codes.InvalidArgument, "registration epoch and owner token are required")
	}
	ownerStore, ok := s.store.(registry.OwnerHeartbeater)
	if !ok {
		return nil, status.Error(codes.Internal, "registry store does not support fenced heartbeat")
	}

	lease := s.leaseTTL(req.LeaseTtlSeconds)
	if err := ownerStore.HeartbeatWithOwner(ctx, req.Service, req.InstanceId, req.RegistrationEpoch, req.OwnerToken, lease); err != nil {
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

// ListInstances returns the current registry view with health state overlaid.
func (s *RegistryService) ListInstances(ctx context.Context, req *aegisv1.ListInstancesRequest) (*aegisv1.ListInstancesResponse, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_ListInstances_FullMethodName, req); err != nil {
		return nil, err
	}
	if req == nil || req.Service == "" {
		return nil, status.Error(codes.InvalidArgument, "service is required")
	}

	view, err := s.instancesView(ctx, req.Service)
	if err != nil {
		return nil, err
	}
	return view.response, nil
}

// WatchInstances streams registry or health changes, falling back to polling when watches close.
func (s *RegistryService) WatchInstances(req *aegisv1.WatchInstancesRequest, stream aegisv1.RegistryService_WatchInstancesServer) error {
	if err := security.AuthorizeControllerPrincipal(stream.Context(), aegisv1.RegistryService_WatchInstances_FullMethodName, req); err != nil {
		return err
	}
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
	// The fallback ticker is lazy: pure watch mode stays event-driven until one watch is unavailable.
	var ticker *time.Ticker
	var ticks <-chan time.Time
	ensureTicker := func() {
		if ticker != nil {
			return
		}
		ticker = time.NewTicker(registryWatchFallbackInterval)
		ticks = ticker.C
	}
	// Closed watches are retried from the last observed version so clients do not miss later changes.
	restartClosedWatches := func(view instancesView) {
		if registryUpdates == nil {
			registryUpdates = s.watchRegistry(ctx, req.Service, view.registryVersion)
		}
		if s.health != nil && healthUpdates == nil {
			healthUpdates = s.watchHealth(ctx, req.Service, view.healthVersion)
		}
		if registryUpdates == nil || (s.health != nil && healthUpdates == nil) {
			ensureTicker()
		}
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
			restartClosedWatches(view)
		}
		if registryUpdates == nil && (s.health == nil || healthUpdates == nil) {
			ensureTicker()
		}
	}
}

// sendInstancesIfChanged sends only when the combined registry/health version changes.
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

// instancesView joins immutable registry snapshots with current health overlays.
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

// registrySnapshot prefers versioned snapshots and falls back to a list for simple stores.
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

// overlayHealth returns overlay health data for RegistryService callers without handing out mutable receiver state.
func (s *RegistryService) overlayHealth(inst registry.Instance) (registry.Instance, float64) {
	slowScore := 0.0
	if s.health == nil {
		return inst, slowScore
	}
	if provider, ok := s.health.(healthSnapshotProvider); ok {
		if health, ok := provider.Get(inst.Service, inst.ID); ok && healthMatchesInstance(health, inst) {
			inst.Status = health.State
			slowScore = health.SlowScore
		}
		return inst, slowScore
	}
	if state, ok := s.health.HealthState(inst.Service, inst.ID); ok {
		inst.Status = state
	}
	return inst, slowScore
}

// healthMatchesInstance provides the shared health matches instance helper for this package call path.
func healthMatchesInstance(health fault.EndpointHealth, inst registry.Instance) bool {
	return healthMatchesInstanceAddress(health.Address, inst.Address) && healthMatchesInstanceRegistrationEpoch(health.RegistrationEpoch, inst.RegistrationEpoch)
}

// healthMatchesInstanceAddress provides the shared health matches instance address helper for this package call path.
func healthMatchesInstanceAddress(healthAddress, instanceAddress string) bool {
	return healthAddress == "" || instanceAddress == "" || healthAddress == instanceAddress
}

// healthMatchesInstanceRegistrationEpoch provides the shared health matches instance registration epoch helper for this package call path.
func healthMatchesInstanceRegistrationEpoch(healthEpoch, instanceEpoch string) bool {
	return healthEpoch == "" || instanceEpoch == "" || healthEpoch == instanceEpoch
}

// watchRegistry streams registry changes to callers until the source or context closes.
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

// watchHealth streams health changes to callers until the source or context closes.
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

// healthVersion returns health version data for RegistryService callers without handing out mutable receiver state.
func (s *RegistryService) healthVersion(service string) int64 {
	provider, ok := s.health.(healthVersionProvider)
	if !ok {
		return 0
	}
	return provider.HealthVersion(service)
}

// combineInstancesVersion provides the shared combine instances version helper for this package call path.
func combineInstancesVersion(registryVersion, healthVersion int64) int64 {
	version := fnv64Mix(1469598103934665603, uint64(registryVersion))
	version = fnv64Mix(version, uint64(healthVersion))
	version &= 1<<63 - 1
	if version == 0 {
		return 1
	}
	return int64(version)
}

// fnv64Mix provides the shared fnv64 mix helper for this package call path.
func fnv64Mix(hash, value uint64) uint64 {
	const prime uint64 = 1099511628211
	for i := 0; i < 8; i++ {
		hash ^= value & 0xff
		hash *= prime
		value >>= 8
	}
	return hash
}

// leaseTTL returns lease ttl data for RegistryService callers without handing out mutable receiver state.
func (s *RegistryService) leaseTTL(seconds int64) time.Duration {
	if seconds <= 0 {
		return s.defaultLease
	}
	return time.Duration(seconds) * time.Second
}

// instanceFromProto provides the shared instance from proto helper for this package call path.
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

// instanceToProto provides the shared instance to proto helper for this package call path.
func instanceToProto(inst registry.Instance) *aegisv1.ServiceInstance {
	return &aegisv1.ServiceInstance{
		Id:                 inst.ID,
		Service:            inst.Service,
		Address:            inst.Address,
		Status:             inst.Status.String(),
		Labels:             cloneProtoLabels(inst.Labels),
		LastSeenUnixMillis: inst.LastSeen.UnixMilli(),
		RegistrationEpoch:  inst.RegistrationEpoch,
	}
}

// cloneProtoLabels returns an isolated copy of clone proto labels input so callers cannot mutate shared state.
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

// statusFromRegistryError provides the shared status from registry error helper for this package call path.
func statusFromRegistryError(err error) error {
	switch err {
	case nil:
		return nil
	case registry.ErrInvalidInstance:
		return status.Error(codes.InvalidArgument, err.Error())
	case registry.ErrInstanceNotFound:
		return status.Error(codes.NotFound, err.Error())
	case registry.ErrRegistrationEpochMismatch:
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

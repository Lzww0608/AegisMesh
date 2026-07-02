package controller

import (
	"context"
	"log"
	"net"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type HealthMetricsSink interface {
	RecordHealth(health fault.EndpointHealth)
}

type TelemetryService struct {
	aegisv1.UnimplementedTelemetryServiceServer

	store       registry.Registry
	manager     *fault.HealthManager
	metrics     HealthMetricsSink
	healthStore fault.HealthSnapshotStore
}

func NewTelemetryService(store registry.Registry, manager *fault.HealthManager, metrics HealthMetricsSink) *TelemetryService {
	return NewTelemetryServiceWithHealthStore(store, manager, metrics, nil)
}

func NewTelemetryServiceWithHealthStore(store registry.Registry, manager *fault.HealthManager, metrics HealthMetricsSink, healthStore fault.HealthSnapshotStore) *TelemetryService {
	if manager == nil {
		manager = fault.NewHealthManager(fault.HealthManagerConfig{})
	}
	return &TelemetryService{
		store:       store,
		manager:     manager,
		metrics:     metrics,
		healthStore: healthStore,
	}
}

func (s *TelemetryService) ReportEndpointStats(ctx context.Context, req *aegisv1.ReportEndpointStatsRequest) (*aegisv1.ReportEndpointStatsResponse, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.TelemetryService_ReportEndpointStats_FullMethodName, req); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "telemetry request is required")
	}

	samples, err := s.samplesFromProto(ctx, req.Samples)
	if err != nil {
		return nil, err
	}
	health := s.manager.Update(samples)
	s.recordHealth(health)
	s.saveHealthSnapshot(ctx, health)
	return &aegisv1.ReportEndpointStatsResponse{Endpoints: healthToProto(health)}, nil
}

func (s *TelemetryService) ListEndpointHealth(ctx context.Context, req *aegisv1.ListEndpointHealthRequest) (*aegisv1.ListEndpointHealthResponse, error) {
	if err := security.AuthorizeControllerPrincipal(ctx, aegisv1.TelemetryService_ListEndpointHealth_FullMethodName, req); err != nil {
		return nil, err
	}
	service := ""
	if req != nil {
		service = req.Service
	}
	health := s.manager.List(service)
	return &aegisv1.ListEndpointHealthResponse{Endpoints: healthToProto(health)}, nil
}

func (s *TelemetryService) samplesFromProto(ctx context.Context, protoSamples []*aegisv1.EndpointStatsSample) ([]fault.EndpointSample, error) {
	samples := make([]fault.EndpointSample, 0, len(protoSamples))
	instanceCache := make(map[string]telemetryServiceInstances)
	for _, protoSample := range protoSamples {
		if protoSample == nil || protoSample.Service == "" {
			continue
		}
		inst, ok, err := s.resolveTelemetryInstance(ctx, instanceCache, protoSample)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if protoSample.RegistrationEpoch != "" && inst.RegistrationEpoch != "" && protoSample.RegistrationEpoch != inst.RegistrationEpoch {
			continue
		}
		address := protoSample.EndpointAddress
		if address == "" {
			address = inst.Address
		}
		registrationEpoch := protoSample.RegistrationEpoch
		if registrationEpoch == "" {
			registrationEpoch = inst.RegistrationEpoch
		}
		samples = append(samples, fault.EndpointSample{
			Service:           protoSample.Service,
			InstanceID:        inst.ID,
			Address:           address,
			RegistrationEpoch: registrationEpoch,
			Method:            protoSample.Method,
			RequestCount:      protoSample.RequestCount,
			ErrorCount:        protoSample.ErrorCount,
			TimeoutCount:      protoSample.TimeoutCount,
			Inflight:          protoSample.Inflight,
			Capacity:          protoSample.Capacity,
			LatencyEWMA:       secondsToDuration(protoSample.LatencyEwmaSeconds),
			LatencyP95:        secondsToDuration(protoSample.LatencyP95Seconds),
			TCPRetransmit:     protoSample.TcpRetransmit,
			ConnectError:      protoSample.ConnectError,
		})
	}
	return samples, nil
}

type telemetryServiceInstances struct {
	byAddress map[string]registry.Instance
	byID      map[string]registry.Instance
}

func (s *TelemetryService) resolveTelemetryInstance(ctx context.Context, cache map[string]telemetryServiceInstances, protoSample *aegisv1.EndpointStatsSample) (registry.Instance, bool, error) {
	instances, err := s.telemetryInstances(ctx, cache, protoSample.Service)
	if err != nil {
		return registry.Instance{}, false, err
	}
	if protoSample.InstanceId != "" {
		inst, ok := instances.byID[protoSample.InstanceId]
		if !ok {
			return registry.Instance{}, false, nil
		}
		if protoSample.EndpointAddress != "" && !healthMatchesInstanceAddress(protoSample.EndpointAddress, inst.Address) {
			return registry.Instance{}, false, nil
		}
		return inst, true, nil
	}
	if protoSample.EndpointAddress == "" {
		return registry.Instance{}, false, nil
	}
	if inst, ok := instances.byAddress[protoSample.EndpointAddress]; ok {
		return inst, true, nil
	}
	inst, ok := resolveInstanceByUniquePort(instances.byAddress, protoSample.EndpointAddress)
	return inst, ok, nil
}

func (s *TelemetryService) telemetryInstances(ctx context.Context, cache map[string]telemetryServiceInstances, service string) (telemetryServiceInstances, error) {
	if service == "" {
		return telemetryServiceInstances{}, nil
	}
	if instances, ok := cache[service]; ok {
		return instances, nil
	}
	listed, err := s.store.List(ctx, service)
	if err != nil {
		return telemetryServiceInstances{}, statusFromRegistryError(err)
	}
	instances := telemetryServiceInstances{
		byAddress: make(map[string]registry.Instance, len(listed)),
		byID:      make(map[string]registry.Instance, len(listed)),
	}
	for _, inst := range listed {
		if inst.ID != "" {
			instances.byID[inst.ID] = inst
		}
		if inst.Address != "" {
			instances.byAddress[inst.Address] = inst
		}
	}
	cache[service] = instances
	return instances, nil
}

func resolveInstanceByUniquePort(byAddress map[string]registry.Instance, observedAddress string) (registry.Instance, bool) {
	_, observedPort, err := net.SplitHostPort(observedAddress)
	if err != nil || observedPort == "" {
		return registry.Instance{}, false
	}

	var match registry.Instance
	for registeredAddress, inst := range byAddress {
		_, registeredPort, err := net.SplitHostPort(registeredAddress)
		if err != nil || registeredPort != observedPort {
			continue
		}
		if match.ID != "" && match.ID != inst.ID {
			return registry.Instance{}, false
		}
		match = inst
	}
	if match.ID == "" {
		return registry.Instance{}, false
	}
	return match, true
}

func (s *TelemetryService) resolveInstanceID(ctx context.Context, cache map[string]map[string]string, service, address string) (string, error) {
	if service == "" || address == "" {
		return "", nil
	}
	byAddress := cache[service]
	if byAddress == nil {
		instances, err := s.store.List(ctx, service)
		if err != nil {
			return "", statusFromRegistryError(err)
		}
		byAddress = make(map[string]string, len(instances))
		for _, inst := range instances {
			byAddress[inst.Address] = inst.ID
		}
		cache[service] = byAddress
	}
	if instanceID := byAddress[address]; instanceID != "" {
		return instanceID, nil
	}
	return resolveInstanceIDByUniquePort(byAddress, address), nil
}

func resolveInstanceIDByUniquePort(byAddress map[string]string, observedAddress string) string {
	_, observedPort, err := net.SplitHostPort(observedAddress)
	if err != nil || observedPort == "" {
		return ""
	}

	var match string
	for registeredAddress, instanceID := range byAddress {
		_, registeredPort, err := net.SplitHostPort(registeredAddress)
		if err != nil || registeredPort != observedPort {
			continue
		}
		if match != "" && match != instanceID {
			return ""
		}
		match = instanceID
	}
	return match
}
func (s *TelemetryService) recordHealth(health []fault.EndpointHealth) {
	if s.metrics == nil {
		return
	}
	for _, endpoint := range health {
		s.metrics.RecordHealth(endpoint)
	}
}
func (s *TelemetryService) saveHealthSnapshot(ctx context.Context, health []fault.EndpointHealth) {
	if s.healthStore == nil || len(health) == 0 {
		return
	}
	if _, err := s.healthStore.Save(ctx, health); err != nil {
		log.Printf("persist health snapshot: %v", err)
	}
}

func healthToProto(health []fault.EndpointHealth) []*aegisv1.EndpointHealth {
	out := make([]*aegisv1.EndpointHealth, 0, len(health))
	for _, endpoint := range health {
		out = append(out, &aegisv1.EndpointHealth{
			Service:                 endpoint.Service,
			InstanceId:              endpoint.InstanceID,
			EndpointAddress:         endpoint.Address,
			RegistrationEpoch:       endpoint.RegistrationEpoch,
			State:                   endpoint.State.String(),
			SlowScore:               endpoint.SlowScore,
			ConsecutiveSlowWindows:  int64(endpoint.ConsecutiveSlowWindows),
			ConsecutiveEjectWindows: int64(endpoint.ConsecutiveEjectWindows),
		})
	}
	return out
}

func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

package controller

import (
	"context"
	"net"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type HealthMetricsSink interface {
	RecordHealth(health fault.EndpointHealth)
}

type TelemetryService struct {
	aegisv1.UnimplementedTelemetryServiceServer

	store   registry.Registry
	manager *fault.HealthManager
	metrics HealthMetricsSink
}

func NewTelemetryService(store registry.Registry, manager *fault.HealthManager, metrics HealthMetricsSink) *TelemetryService {
	if manager == nil {
		manager = fault.NewHealthManager(fault.HealthManagerConfig{})
	}
	return &TelemetryService{
		store:   store,
		manager: manager,
		metrics: metrics,
	}
}

func (s *TelemetryService) ReportEndpointStats(ctx context.Context, req *aegisv1.ReportEndpointStatsRequest) (*aegisv1.ReportEndpointStatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "telemetry request is required")
	}

	samples, err := s.samplesFromProto(ctx, req.Samples)
	if err != nil {
		return nil, err
	}
	health := s.manager.Update(samples)
	s.recordHealth(health)
	return &aegisv1.ReportEndpointStatsResponse{Endpoints: healthToProto(health)}, nil
}

func (s *TelemetryService) ListEndpointHealth(_ context.Context, req *aegisv1.ListEndpointHealthRequest) (*aegisv1.ListEndpointHealthResponse, error) {
	service := ""
	if req != nil {
		service = req.Service
	}
	health := s.manager.List(service)
	return &aegisv1.ListEndpointHealthResponse{Endpoints: healthToProto(health)}, nil
}

func (s *TelemetryService) samplesFromProto(ctx context.Context, protoSamples []*aegisv1.EndpointStatsSample) ([]fault.EndpointSample, error) {
	samples := make([]fault.EndpointSample, 0, len(protoSamples))
	instanceCache := make(map[string]map[string]string)
	for _, protoSample := range protoSamples {
		if protoSample == nil {
			continue
		}
		instanceID := protoSample.InstanceId
		if instanceID == "" {
			resolved, err := s.resolveInstanceID(ctx, instanceCache, protoSample.Service, protoSample.EndpointAddress)
			if err != nil {
				return nil, err
			}
			instanceID = resolved
		}
		if protoSample.Service == "" || instanceID == "" {
			continue
		}
		samples = append(samples, fault.EndpointSample{
			Service:       protoSample.Service,
			InstanceID:    instanceID,
			Address:       protoSample.EndpointAddress,
			Method:        protoSample.Method,
			RequestCount:  protoSample.RequestCount,
			ErrorCount:    protoSample.ErrorCount,
			TimeoutCount:  protoSample.TimeoutCount,
			Inflight:      protoSample.Inflight,
			Capacity:      protoSample.Capacity,
			LatencyEWMA:   secondsToDuration(protoSample.LatencyEwmaSeconds),
			LatencyP95:    secondsToDuration(protoSample.LatencyP95Seconds),
			TCPRetransmit: protoSample.TcpRetransmit,
			ConnectError:  protoSample.ConnectError,
		})
	}
	return samples, nil
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

func healthToProto(health []fault.EndpointHealth) []*aegisv1.EndpointHealth {
	out := make([]*aegisv1.EndpointHealth, 0, len(health))
	for _, endpoint := range health {
		out = append(out, &aegisv1.EndpointHealth{
			Service:                 endpoint.Service,
			InstanceId:              endpoint.InstanceID,
			EndpointAddress:         endpoint.Address,
			State:                   string(endpoint.State),
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

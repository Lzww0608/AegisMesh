package aegisgrpc

import (
	"context"
	"errors"
	"net/url"
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
)

const (
	Scheme = "aegis"

	adaptiveP2CServiceConfig = `{"loadBalancingConfig":[{"aegis_adaptive_p2c":{}}]}`
	roundRobinServiceConfig  = `{"loadBalancingConfig":[{"round_robin":{}}]}`
)

var registerOnce sync.Once

// TargetForService provides the shared target for service helper for resolver, picker, and reporter state.
func TargetForService(controllerAddr, service string) string {
	return targetForServiceWithControlPlaneConfig(controllerAddr, service, "", "", "")
}

// targetForServiceWithLimiterPool provides the shared target for service with limiter pool helper for resolver, picker, and reporter state.
func targetForServiceWithLimiterPool(controllerAddr, service, limiterPoolID string) string {
	return targetForServiceWithControlPlaneConfig(controllerAddr, service, limiterPoolID, "", "")
}

// targetForServiceWithControlPlaneConfig provides the shared target for service with control plane config helper for resolver, picker, and reporter state.
func targetForServiceWithControlPlaneConfig(controllerAddr, service, limiterPoolID, controllerSecurityID, controllerAddressesID string) string {
	target := &url.URL{
		Scheme: Scheme,
		Host:   controllerAddr,
		Path:   "/" + service,
	}
	query := target.Query()
	if limiterPoolID != "" {
		query.Set(adaptiveLimiterPoolTargetKey, limiterPoolID)
	}
	if controllerSecurityID != "" {
		query.Set(controllerSecurityTargetKey, controllerSecurityID)
	}
	if controllerAddressesID != "" {
		query.Set(controllerAddressesTargetKey, controllerAddressesID)
	}
	target.RawQuery = query.Encode()
	return target.String()
}

// DialService opens a gRPC connection using AegisMesh controller and security options.
func DialService(ctx context.Context, controllerAddr, service string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return DialServiceFrom(ctx, "unknown", controllerAddr, service, opts...)
}

// DialServiceFrom opens a gRPC connection using AegisMesh controller and security options.
func DialServiceFrom(ctx context.Context, source, controllerAddr, service string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return DialServiceFromWithOptions(ctx, source, controllerAddr, service, DefaultDialOptions(), opts...)
}

// DialServiceFromWithOptions opens a gRPC connection using AegisMesh controller and security options.
func DialServiceFromWithOptions(ctx context.Context, source, controllerAddr, service string, options DialOptions, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	registerDefaultResolver()
	registerDefaultBalancer()
	options = normalizeDialOptions(options)

	controllerAddrs := effectiveControllerAddresses(controllerAddr, options)
	if len(controllerAddrs) == 0 {
		return nil, errors.New("controller address is required")
	}
	controllerSecurity := effectiveControllerSecurityConfig(options)
	controlPlaneDialOptions, err := controllerDialOptions(controllerSecurity)
	if err != nil {
		return nil, err
	}
	controllerAddressesID := registerControllerAddresses(controllerAddrs)
	controllerAddressesOwned := true
	defer func() {
		if controllerAddressesOwned {
			unregisterControllerAddresses(controllerAddressesID)
		}
	}()
	controlPlaneTarget := controllerTargetForAddressesID(controllerAddressesID)

	limiterPool := newAdaptiveLimiterPool(adaptiveDefaultMaxInflightPerTarget)
	policies := &policyManager{circuitBreaker: limiterPool}
	if !options.DisablePolicy {
		if snapshot := loadInitialPolicy(ctx, controlPlaneTarget, service, policies, controlPlaneDialOptions); snapshot != nil {
			options = applyPolicySnapshotToDialOptions(options, snapshot)
		}
	}

	serviceConfig, err := serviceConfigForRoutingPolicy(options.RoutingPolicy)
	if err != nil {
		return nil, err
	}

	var recorder *telemetry.Recorder
	if !options.DisableTelemetry {
		recorder = telemetry.NewRecorder(source, telemetry.DefaultPrometheusMetrics())
	}
	tracer, err := traceWriterFromOptions(options)
	if err != nil {
		return nil, err
	}
	retrySource := newDynamicRetrySource(options, policies)

	transportCredentials := options.TransportCredentials
	if transportCredentials == nil {
		transportCredentials = insecure.NewCredentials()
	}
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithDefaultServiceConfig(serviceConfig),
		grpc.WithChainUnaryInterceptor(
			newRetryUnaryInterceptorFromSource(retrySource),
			newTelemetryUnaryInterceptor(source, service, recorder, tracer),
		),
	}
	dialOptions = append(dialOptions, opts...)

	limiterPoolID := registerAdaptiveLimiterPool(limiterPool)
	controllerSecurityID := registerControllerSecurityConfig(controllerSecurity)
	conn, err := grpc.DialContext(ctx, targetForServiceWithControlPlaneConfig(controllerAddrs[0], service, limiterPoolID, controllerSecurityID, controllerAddressesID), dialOptions...)
	if err != nil {
		unregisterAdaptiveLimiterPool(limiterPoolID)
		unregisterControllerSecurityConfig(controllerSecurityID)
		unregisterControllerAddresses(controllerAddressesID)
		return nil, err
	}
	controllerAddressesOwned = false
	connCtx := contextForClientConn(conn)
	if !options.DisablePolicy {
		startPolicyWatcher(connCtx, controlPlaneTarget, service, policies, controlPlaneDialOptions)
	}
	if recorder != nil {
		startReporter(connCtx, controlPlaneTarget, recorder, controlPlaneDialOptions)
	}
	return conn, nil
}

// startReporter launches telemetry reporting on the control-plane connection owned by Dial.
func startReporter(ctx context.Context, controllerAddr string, recorder *telemetry.Recorder, dialOptions []grpc.DialOption) {
	conn, err := grpc.DialContext(ctx, controllerAddr, dialOptions...)
	if err != nil {
		return
	}
	reporter := newTelemetryReporter(aegisv1.NewTelemetryServiceClient(conn), recorder, defaultTelemetryReportInterval)
	go func() {
		defer conn.Close()
		reporter.Run(ctx)
	}()
}

// contextForClientConn provides the shared context for client conn helper for resolver, picker, and reporter state.
func contextForClientConn(conn *grpc.ClientConn) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		for {
			state := conn.GetState()
			if state == connectivity.Shutdown {
				return
			}
			if !conn.WaitForStateChange(context.Background(), state) {
				return
			}
		}
	}()
	return ctx
}

// registerDefaultResolver registers register default resolver with the controller or local registry.
func registerDefaultResolver() {
	registerOnce.Do(func() {
		resolver.Register(newRegistryResolverBuilder())
		resolver.Register(newControllerResolverBuilder())
	})
}

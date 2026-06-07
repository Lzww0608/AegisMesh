package aegisgrpc

import (
	"context"
	"net/url"
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
)

const (
	Scheme = "aegis"

	adaptiveP2CServiceConfig = `{"loadBalancingConfig":[{"aegis_adaptive_p2c":{}}]}`
	roundRobinServiceConfig  = `{"loadBalancingConfig":[{"round_robin":{}}]}`
)

var registerOnce sync.Once

func TargetForService(controllerAddr, service string) string {
	return (&url.URL{
		Scheme: Scheme,
		Host:   controllerAddr,
		Path:   "/" + service,
	}).String()
}

func DialService(ctx context.Context, controllerAddr, service string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return DialServiceFrom(ctx, "unknown", controllerAddr, service, opts...)
}

func DialServiceFrom(ctx context.Context, source, controllerAddr, service string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return DialServiceFromWithOptions(ctx, source, controllerAddr, service, DefaultDialOptions(), opts...)
}

func DialServiceFromWithOptions(ctx context.Context, source, controllerAddr, service string, options DialOptions, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	registerDefaultResolver()
	registerDefaultBalancer()
	options = normalizeDialOptions(options)

	serviceConfig, err := serviceConfigForRoutingPolicy(options.RoutingPolicy)
	if err != nil {
		return nil, err
	}

	var recorder *telemetry.Recorder
	if !options.DisableTelemetry {
		recorder = telemetry.NewRecorder(source, telemetry.DefaultPrometheusMetrics())
		startReporter(ctx, controllerAddr, recorder)
	}
	retryPolicy, budget := retryComponentsForDialOptions(options)

	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(serviceConfig),
		grpc.WithChainUnaryInterceptor(
			newRetryUnaryInterceptor(retryPolicy, budget),
			newTelemetryUnaryInterceptor(service, recorder),
		),
	}
	dialOptions = append(dialOptions, opts...)

	return grpc.DialContext(ctx, TargetForService(controllerAddr, service), dialOptions...)
}

func startReporter(ctx context.Context, controllerAddr string, recorder *telemetry.Recorder) {
	conn, err := grpc.DialContext(ctx, controllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return
	}
	reporter := newTelemetryReporter(aegisv1.NewTelemetryServiceClient(conn), recorder, defaultTelemetryReportInterval)
	go func() {
		defer conn.Close()
		reporter.Run(ctx)
	}()
}

func registerDefaultResolver() {
	registerOnce.Do(func() {
		resolver.Register(newRegistryResolverBuilder())
	})
}

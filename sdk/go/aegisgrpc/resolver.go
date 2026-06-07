package aegisgrpc

import (
	"context"
	"errors"
	"strings"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
)

const defaultRefreshInterval = 3 * time.Second

type addressAttributeKey string

const (
	instanceIDAttribute addressAttributeKey = "aegis.instance_id"
	statusAttribute     addressAttributeKey = "aegis.status"
	slowScoreAttribute  addressAttributeKey = "aegis.slow_score"
)

type registryResolverBuilder struct {
	refreshInterval time.Duration
}

func newRegistryResolverBuilder() *registryResolverBuilder {
	return &registryResolverBuilder{refreshInterval: defaultRefreshInterval}
}

func (b *registryResolverBuilder) Scheme() string {
	return Scheme
}

func (b *registryResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	controllerAddr, service, err := parseTarget(target)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.DialContext(ctx, controllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		cancel()
		return nil, err
	}

	r := &registryResolver{
		ctx:             ctx,
		cancel:          cancel,
		conn:            conn,
		client:          aegisv1.NewRegistryServiceClient(conn),
		cc:              cc,
		service:         service,
		refreshInterval: b.refreshInterval,
	}
	r.ResolveNow(resolver.ResolveNowOptions{})
	go r.watch()
	return r, nil
}

type registryResolver struct {
	ctx             context.Context
	cancel          context.CancelFunc
	conn            *grpc.ClientConn
	client          aegisv1.RegistryServiceClient
	cc              resolver.ClientConn
	service         string
	refreshInterval time.Duration
}

func (r *registryResolver) ResolveNow(resolver.ResolveNowOptions) {
	r.resolve()
}

func (r *registryResolver) Close() {
	r.cancel()
	_ = r.conn.Close()
}

func (r *registryResolver) watch() {
	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.resolve()
		}
	}
}

func (r *registryResolver) resolve() {
	ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
	defer cancel()

	resp, err := r.client.ListInstances(ctx, &aegisv1.ListInstancesRequest{Service: r.service})
	if err != nil {
		r.cc.ReportError(err)
		return
	}

	if err := r.cc.UpdateState(resolver.State{Addresses: instancesToAddresses(resp.Instances)}); err != nil {
		r.cc.ReportError(err)
	}
}

func parseTarget(target resolver.Target) (string, string, error) {
	if target.URL.Scheme != Scheme {
		return "", "", errors.New("target scheme must be aegis")
	}

	controllerAddr := target.URL.Host
	service := strings.TrimPrefix(target.URL.Path, "/")
	if controllerAddr == "" {
		return "", "", errors.New("controller address is required")
	}
	if service == "" {
		return "", "", errors.New("service name is required")
	}
	return controllerAddr, service, nil
}

func instancesToAddresses(instances []*aegisv1.ServiceInstance) []resolver.Address {
	addresses := make([]resolver.Address, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.Address == "" {
			continue
		}
		switch inst.Status {
		case "", "HEALTHY", "DEGRADED", "PROBING":
			addresses = append(addresses, resolver.Address{
				Addr:       inst.Address,
				ServerName: inst.Id,
				Attributes: addressAttributes(inst.Id, inst.Status, inst.SlowScore),
			})
		}
	}
	return addresses
}

func addressAttributes(instanceID, status string, slowScore float64) *attributes.Attributes {
	return attributes.New(instanceIDAttribute, instanceID).
		WithValue(statusAttribute, status).
		WithValue(slowScoreAttribute, slowScore)
}

func instanceIDFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(instanceIDAttribute).(string)
	return value
}

func endpointStatusFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(statusAttribute).(string)
	return value
}

func slowScoreFromAttributes(attrs *attributes.Attributes) float64 {
	if attrs == nil {
		return 0
	}
	value, _ := attrs.Value(slowScoreAttribute).(float64)
	return value
}

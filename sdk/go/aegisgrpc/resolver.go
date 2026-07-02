package aegisgrpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	aegisstatus "github.com/aegismesh/aegismesh/pkg/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
)

const (
	defaultRefreshInterval     = 3 * time.Second
	maxWatchRetryBackoff       = 30 * time.Second
	maxWatchFailuresBeforePoll = 5
	pollTicksBeforeWatchRetry  = 5
)

var endpointIdentityByAddress sync.Map

type endpointIdentity struct {
	instanceID        string
	registrationEpoch string
}

type addressAttributeKey string

const (
	instanceIDAttribute        addressAttributeKey = "aegis.instance_id"
	registrationEpochAttribute addressAttributeKey = "aegis.registration_epoch"
	statusAttribute            addressAttributeKey = "aegis.status"
	slowScoreAttribute         addressAttributeKey = "aegis.slow_score"
	limiterPoolAttribute       addressAttributeKey = "aegis.limiter_pool"
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
	limiterPoolID := target.URL.Query().Get(adaptiveLimiterPoolTargetKey)
	limiterPool := loadAdaptiveLimiterPool(limiterPoolID)
	if limiterPool == nil {
		limiterPool = newAdaptiveLimiterPool(adaptiveDefaultMaxInflightPerTarget)
	}
	controllerSecurityID := target.URL.Query().Get(controllerSecurityTargetKey)
	controllerAddressesID := target.URL.Query().Get(controllerAddressesTargetKey)
	controllerAddressesOwned := false
	if controllerAddressesID == "" {
		controllerAddressesID = registerControllerAddresses(splitControllerAddresses(controllerAddr))
		controllerAddressesOwned = true
	}
	cleanupBuildFailure := func() {
		if controllerAddressesOwned {
			unregisterControllerAddresses(controllerAddressesID)
		}
	}
	controllerSecurity, ok := loadControllerSecurityConfig(controllerSecurityID)
	if !ok {
		cleanupBuildFailure()
		return nil, errors.New("controller security configuration was not found")
	}
	controllerDialOptions, err := controllerDialOptions(controllerSecurity)
	if err != nil {
		cleanupBuildFailure()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.DialContext(ctx, controllerTargetForAddressesID(controllerAddressesID), controllerDialOptions...)
	if err != nil {
		cancel()
		cleanupBuildFailure()
		return nil, err
	}

	r := &registryResolver{
		ctx:                   ctx,
		cancel:                cancel,
		conn:                  conn,
		client:                aegisv1.NewRegistryServiceClient(conn),
		cc:                    cc,
		service:               service,
		limiterPoolID:         limiterPoolID,
		controllerSecurityID:  controllerSecurityID,
		controllerAddressesID: controllerAddressesID,
		limiterPool:           limiterPool,
		refreshInterval:       b.refreshInterval,
		watchBackoff:          b.refreshInterval,
	}
	r.ResolveNow(resolver.ResolveNowOptions{})
	go r.watch()
	return r, nil
}

type registryResolver struct {
	ctx                   context.Context
	cancel                context.CancelFunc
	conn                  *grpc.ClientConn
	client                aegisv1.RegistryServiceClient
	cc                    resolver.ClientConn
	service               string
	limiterPoolID         string
	controllerSecurityID  string
	controllerAddressesID string
	limiterPool           *adaptiveLimiterPool
	refreshInterval       time.Duration

	mu            sync.Mutex
	lastVersion   int64
	watchFailures int
	watchBackoff  time.Duration
}

func (r *registryResolver) ResolveNow(resolver.ResolveNowOptions) {
	r.resolve()
}

func (r *registryResolver) Close() {
	r.cancel()
	_ = r.conn.Close()
	unregisterAdaptiveLimiterPool(r.limiterPoolID)
	unregisterControllerSecurityConfig(r.controllerSecurityID)
	unregisterControllerAddresses(r.controllerAddressesID)
}

func (r *registryResolver) watch() {
	for {
		err := r.watchStream()
		if err == nil || r.ctx.Err() != nil {
			return
		}
		if status.Code(err) == codes.Unimplemented {
			r.pollLoop(false)
			return
		}

		r.recordWatchFailure()
		if !errors.Is(err, io.EOF) {
			r.cc.ReportError(err)
		}
		r.resolve()
		if r.watchFailures >= maxWatchFailuresBeforePoll {
			if !r.pollLoop(true) {
				return
			}
			r.resetWatchRetry()
			continue
		}
		if !r.waitWatchBackoff() {
			return
		}
	}
}

func (r *registryResolver) watchStream() error {
	stream, err := r.client.WatchInstances(r.ctx, &aegisv1.WatchInstancesRequest{
		Service:         r.service,
		LastSeenVersion: r.currentVersion(),
	})
	if err != nil {
		return err
	}
	r.resetWatchRetry()

	for {
		resp, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := r.applyInstancesResponse(resp); err != nil {
			r.cc.ReportError(err)
		}
	}
}

func (r *registryResolver) recordWatchFailure() {
	r.mu.Lock()
	r.watchFailures++
	r.mu.Unlock()
}

func (r *registryResolver) resetWatchRetry() {
	r.mu.Lock()
	r.watchFailures = 0
	r.watchBackoff = r.refreshInterval
	r.mu.Unlock()
}

func (r *registryResolver) waitWatchBackoff() bool {
	delay := r.nextWatchRetryDelay()
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-r.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *registryResolver) nextWatchRetryDelay() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	delay := r.watchBackoff
	next := r.watchBackoff * 2
	if next > maxWatchRetryBackoff {
		next = maxWatchRetryBackoff
	}
	r.watchBackoff = next
	return delay
}

func (r *registryResolver) pollLoop(retryWatch bool) bool {
	r.resolve()
	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()

	polls := 0
	for {
		select {
		case <-r.ctx.Done():
			return false
		case <-ticker.C:
			r.resolve()
			polls++
			if retryWatch && polls >= pollTicksBeforeWatchRetry {
				return true
			}
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

	if err := r.applyInstancesResponse(resp); err != nil {
		r.cc.ReportError(err)
	}
}

func (r *registryResolver) applyInstancesResponse(resp *aegisv1.ListInstancesResponse) error {
	if resp == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if resp.Version != 0 && resp.Version == r.lastVersion {
		return nil
	}
	if err := r.cc.UpdateState(resolver.State{Addresses: instancesToAddressesWithLimiterPool(resp.Instances, r.limiterPool)}); err != nil {
		return err
	}
	if resp.Version != 0 {
		r.lastVersion = resp.Version
	}
	return nil
}

func (r *registryResolver) currentVersion() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lastVersion
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
	return instancesToAddressesWithLimiterPool(instances, nil)
}

func instancesToAddressesWithLimiterPool(instances []*aegisv1.ServiceInstance, limiterPool *adaptiveLimiterPool) []resolver.Address {
	addresses := make([]resolver.Address, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.Address == "" {
			continue
		}
		if !aegisstatus.Parse(inst.Status).Routable() {
			continue
		}
		rememberEndpointIdentity(inst.Address, inst.Id, inst.RegistrationEpoch)
		addresses = append(addresses, resolver.Address{
			Addr:       inst.Address,
			ServerName: inst.Id,
			Attributes: addressAttributesWithLimiterPoolAndEpoch(inst.Id, inst.RegistrationEpoch, aegisstatus.Parse(inst.Status), inst.SlowScore, limiterPool),
		})
	}
	return addresses
}

func rememberEndpointIdentity(address, endpointID, registrationEpoch string) {
	if address == "" {
		return
	}
	if endpointID == "" {
		endpointIdentityByAddress.Delete(address)
		return
	}
	endpointIdentityByAddress.Store(address, endpointIdentity{instanceID: endpointID, registrationEpoch: registrationEpoch})
}

func endpointIdentityForAddress(address string) endpointIdentity {
	value, _ := endpointIdentityByAddress.Load(address)
	if identity, ok := value.(endpointIdentity); ok {
		return identity
	}
	if endpointID, ok := value.(string); ok {
		return endpointIdentity{instanceID: endpointID}
	}
	return endpointIdentity{}
}

func endpointIDForAddress(address string) string {
	return endpointIdentityForAddress(address).instanceID
}

func addressAttributes(instanceID string, statusCode aegisstatus.Code, slowScore float64) *attributes.Attributes {
	return addressAttributesWithLimiterPool(instanceID, statusCode, slowScore, nil)
}

func addressAttributesWithLimiterPool(instanceID string, statusCode aegisstatus.Code, slowScore float64, limiterPool *adaptiveLimiterPool) *attributes.Attributes {
	return addressAttributesWithLimiterPoolAndEpoch(instanceID, "", statusCode, slowScore, limiterPool)
}

func addressAttributesWithLimiterPoolAndEpoch(instanceID, registrationEpoch string, statusCode aegisstatus.Code, slowScore float64, limiterPool *adaptiveLimiterPool) *attributes.Attributes {
	attrs := attributes.New(instanceIDAttribute, instanceID).
		WithValue(registrationEpochAttribute, registrationEpoch).
		WithValue(statusAttribute, statusCode).
		WithValue(slowScoreAttribute, slowScore)
	if limiterPool != nil {
		attrs = attrs.WithValue(limiterPoolAttribute, limiterPool)
	}
	return attrs
}

func limiterPoolFromAttributes(attrs *attributes.Attributes) *adaptiveLimiterPool {
	if attrs == nil {
		return nil
	}
	pool, _ := attrs.Value(limiterPoolAttribute).(*adaptiveLimiterPool)
	return pool
}

func instanceIDFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(instanceIDAttribute).(string)
	return value
}
func registrationEpochFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(registrationEpochAttribute).(string)
	return value
}

func endpointStatusFromAttributes(attrs *attributes.Attributes) aegisstatus.Code {
	if attrs == nil {
		return aegisstatus.Unspecified
	}
	if value, ok := attrs.Value(statusAttribute).(aegisstatus.Code); ok {
		return value
	}
	if value, ok := attrs.Value(statusAttribute).(string); ok {
		return aegisstatus.Parse(value)
	}
	return aegisstatus.Unspecified
}

func slowScoreFromAttributes(attrs *attributes.Attributes) float64 {
	if attrs == nil {
		return 0
	}
	value, _ := attrs.Value(slowScoreAttribute).(float64)
	return value
}

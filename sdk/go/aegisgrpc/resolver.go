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

// endpointIdentity carries endpoint identity state for resolver, picker, and reporter state.
type endpointIdentity struct {
	instanceID        string
	registrationEpoch string
}

// addressAttributeKey names the address attribute key values accepted by resolver, picker, and reporter state.
type addressAttributeKey string

const (
	// instanceIDAttribute identifies the instance id attribute constant used by this package.
	instanceIDAttribute        addressAttributeKey = "aegis.instance_id"
	registrationEpochAttribute addressAttributeKey = "aegis.registration_epoch"
	statusAttribute            addressAttributeKey = "aegis.status"
	slowScoreAttribute         addressAttributeKey = "aegis.slow_score"
	limiterPoolAttribute       addressAttributeKey = "aegis.limiter_pool"
)

// registryResolverBuilder carries registry resolver builder state for resolver, picker, and reporter state.
type registryResolverBuilder struct {
	refreshInterval time.Duration
}

// newRegistryResolverBuilder initializes registry resolver builder with package defaults for this package's call path.
func newRegistryResolverBuilder() *registryResolverBuilder {
	return &registryResolverBuilder{refreshInterval: defaultRefreshInterval}
}

// Scheme returns the resolver scheme registered with gRPC.
func (b *registryResolverBuilder) Scheme() string {
	return Scheme
}

// Build parses the target, opens the controller connection, and starts registry watching.
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
	// Resolver targets carry process-local IDs; failures must unregister owned IDs to avoid leaks.
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

// registryResolver carries registry resolver state for resolver, picker, and reporter state.
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

// ResolveNow forces an immediate ListInstances refresh from the controller.
func (r *registryResolver) ResolveNow(resolver.ResolveNowOptions) {
	r.resolve()
}

// Close stops watch/poll loops and releases process-local resolver handles.
func (r *registryResolver) Close() {
	r.cancel()
	_ = r.conn.Close()
	unregisterAdaptiveLimiterPool(r.limiterPoolID)
	unregisterControllerSecurityConfig(r.controllerSecurityID)
	unregisterControllerAddresses(r.controllerAddressesID)
}

// watch streams backing-source changes to callers until the source or context closes.
func (r *registryResolver) watch() {
	for {
		err := r.watchStream()
		if err == nil || r.ctx.Err() != nil {
			return
		}
		// Older controllers may not implement WatchInstances; polling keeps discovery compatible.
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

// watchStream streams stream changes to callers until the source or context closes.
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

// recordWatchFailure increments the consecutive streaming failure counter.
func (r *registryResolver) recordWatchFailure() {
	r.mu.Lock()
	r.watchFailures++
	r.mu.Unlock()
}

// resetWatchRetry clears streaming failure state after a successful watch.
func (r *registryResolver) resetWatchRetry() {
	r.mu.Lock()
	r.watchFailures = 0
	r.watchBackoff = r.refreshInterval
	r.mu.Unlock()
}

// waitWatchBackoff waits for wait watch backoff to reach the expected state or timeout.
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

// nextWatchRetryDelay returns next watch retry delay data for registryResolver callers without handing out mutable receiver state.
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

// pollLoop periodically lists instances and optionally returns to retry streaming.
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

// resolve refreshes resolver state from the controller.
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

// applyInstancesResponse applies apply instances response to the mutable target while preserving transition rules.
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

// currentVersion returns current version data for registryResolver callers without handing out mutable receiver state.
func (r *registryResolver) currentVersion() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lastVersion
}

// parseTarget decodes target input into the package's typed representation.
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

// instancesToAddresses converts registry instances into resolver addresses with fresh attributes.
func instancesToAddresses(instances []*aegisv1.ServiceInstance) []resolver.Address {
	return instancesToAddressesWithLimiterPool(instances, nil)
}

// instancesToAddressesWithLimiterPool attaches stable limiter pools while rebuilding resolver addresses.
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

// rememberEndpointIdentity caches address-to-identity metadata for telemetry emitted after picks.
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

// endpointIdentityForAddress returns cached identity metadata or falls back to the raw address.
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

// endpointIDForAddress preserves legacy address identity when the registry has no instance id.
func endpointIDForAddress(address string) string {
	return endpointIdentityForAddress(address).instanceID
}

// addressAttributes stores health state on resolver addresses for the adaptive picker.
func addressAttributes(instanceID string, statusCode aegisstatus.Code, slowScore float64) *attributes.Attributes {
	return addressAttributesWithLimiterPool(instanceID, statusCode, slowScore, nil)
}

// addressAttributesWithLimiterPool carries limiter state across resolver updates.
func addressAttributesWithLimiterPool(instanceID string, statusCode aegisstatus.Code, slowScore float64, limiterPool *adaptiveLimiterPool) *attributes.Attributes {
	return addressAttributesWithLimiterPoolAndEpoch(instanceID, "", statusCode, slowScore, limiterPool)
}

// addressAttributesWithLimiterPoolAndEpoch carries limiter and epoch fencing data into picker attributes.
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

// limiterPoolFromAttributes recovers shared limiter state from resolver attributes.
func limiterPoolFromAttributes(attrs *attributes.Attributes) *adaptiveLimiterPool {
	if attrs == nil {
		return nil
	}
	pool, _ := attrs.Value(limiterPoolAttribute).(*adaptiveLimiterPool)
	return pool
}

// instanceIDFromAttributes extracts registry identity from resolver attributes for telemetry.
func instanceIDFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(instanceIDAttribute).(string)
	return value
}

// registrationEpochFromAttributes extracts epoch fencing metadata from resolver attributes.
func registrationEpochFromAttributes(attrs *attributes.Attributes) string {
	if attrs == nil {
		return ""
	}
	value, _ := attrs.Value(registrationEpochAttribute).(string)
	return value
}

// endpointStatusFromAttributes defaults missing status to healthy for legacy resolver entries.
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

// slowScoreFromAttributes extracts the fault score used by adaptive endpoint cost.
func slowScoreFromAttributes(attrs *attributes.Attributes) float64 {
	if attrs == nil {
		return 0
	}
	value, _ := attrs.Value(slowScoreAttribute).(float64)
	return value
}

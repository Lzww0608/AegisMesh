package aegisgrpc

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/resolver"
)

const (
	controllerAddressesTargetKey = "controller_addresses"
	controllerResolverScheme     = "aegis-controller"
	controllerFailoverConfig     = `{"loadBalancingConfig":[{"pick_first":{}}]}`
	controllerAddressesEnv       = "AEGIS_CONTROLLER_ADDRS"
)

var (
	// controllerAddressIDs identifies the controller address i ds constant used by this package.
	controllerAddressIDs    atomic.Uint64
	controllerAddressesByID sync.Map
)

// effectiveControllerAddresses provides the shared effective controller addresses helper for resolver, picker, and reporter state.
func effectiveControllerAddresses(controllerAddr string, options DialOptions) []string {
	addrs := splitControllerAddresses(controllerAddr)
	if len(options.ControllerAddrs) > 0 {
		addrs = append(addrs, options.ControllerAddrs...)
	}
	if envAddrs := splitControllerAddresses(os.Getenv(controllerAddressesEnv)); len(envAddrs) > 0 {
		addrs = append(addrs, envAddrs...)
	}
	return dedupeControllerAddresses(addrs)
}

// splitControllerAddresses keeps split controller addresses rules consistent for resolver, picker, and reporter state.
func splitControllerAddresses(raw string) []string {
	items := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// dedupeControllerAddresses provides the shared dedupe controller addresses helper for resolver, picker, and reporter state.
func dedupeControllerAddresses(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

// registerControllerAddresses registers register controller addresses with the controller or local registry.
func registerControllerAddresses(addrs []string) string {
	id := strconv.FormatUint(controllerAddressIDs.Add(1), 10)
	controllerAddressesByID.Store(id, dedupeControllerAddresses(addrs))
	return id
}

// loadControllerAddresses reads controller addresses state from the configured backing source and returns a caller-owned view.
func loadControllerAddresses(id string, fallback string) []string {
	if id != "" {
		if value, ok := controllerAddressesByID.Load(id); ok {
			if addrs, ok := value.([]string); ok {
				return append([]string(nil), addrs...)
			}
		}
	}
	return splitControllerAddresses(fallback)
}

// unregisterControllerAddresses unregisters unregister controller addresses and releases its process-local handle.
func unregisterControllerAddresses(id string) {
	if id != "" {
		controllerAddressesByID.Delete(id)
	}
}

// controllerTargetForAddressesID provides the shared controller target for addresses id helper for resolver, picker, and reporter state.
func controllerTargetForAddressesID(id string) string {
	target := &url.URL{Scheme: controllerResolverScheme, Path: "/" + id}
	return target.String()
}

// controllerResolverBuilder carries controller resolver builder state for resolver, picker, and reporter state.
type controllerResolverBuilder struct{}

// newControllerResolverBuilder initializes controller resolver builder with package defaults for this package's call path.
func newControllerResolverBuilder() *controllerResolverBuilder {
	return &controllerResolverBuilder{}
}

// Scheme returns scheme data for controllerResolverBuilder callers without handing out mutable receiver state.
func (b *controllerResolverBuilder) Scheme() string {
	return controllerResolverScheme
}

// Build builds build dependencies from validated configuration.
func (b *controllerResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	id := strings.Trim(strings.TrimPrefix(target.URL.Path, "/"), "/")
	if id == "" {
		id = target.URL.Host
	}
	addrs := loadControllerAddresses(id, "")
	if len(addrs) == 0 {
		return nil, fmt.Errorf("controller resolver target %q has no registered addresses", id)
	}
	resolved := make([]resolver.Address, 0, len(addrs))
	for _, addr := range addrs {
		resolved = append(resolved, resolver.Address{Addr: addr})
	}
	if err := cc.UpdateState(resolver.State{Addresses: resolved}); err != nil {
		return nil, err
	}
	return controllerResolver{}, nil
}

// controllerResolver carries controller resolver state for resolver, picker, and reporter state.
type controllerResolver struct{}

// ResolveNow refreshes resolver state from the controller.
func (controllerResolver) ResolveNow(resolver.ResolveNowOptions) {}

// Close closes owned resources and makes repeated calls safe.
func (controllerResolver) Close() {}

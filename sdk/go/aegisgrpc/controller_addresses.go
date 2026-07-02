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
	controllerAddressIDs    atomic.Uint64
	controllerAddressesByID sync.Map
)

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

func registerControllerAddresses(addrs []string) string {
	id := strconv.FormatUint(controllerAddressIDs.Add(1), 10)
	controllerAddressesByID.Store(id, dedupeControllerAddresses(addrs))
	return id
}

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

func unregisterControllerAddresses(id string) {
	if id != "" {
		controllerAddressesByID.Delete(id)
	}
}

func controllerTargetForAddressesID(id string) string {
	target := &url.URL{Scheme: controllerResolverScheme, Path: "/" + id}
	return target.String()
}

type controllerResolverBuilder struct{}

func newControllerResolverBuilder() *controllerResolverBuilder {
	return &controllerResolverBuilder{}
}

func (b *controllerResolverBuilder) Scheme() string {
	return controllerResolverScheme
}

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

type controllerResolver struct{}

func (controllerResolver) ResolveNow(resolver.ResolveNowOptions) {}

func (controllerResolver) Close() {}

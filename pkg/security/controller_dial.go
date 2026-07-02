package security

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
)

const (
	ControllerAddressesEnv            = "AEGIS_CONTROLLER_ADDRS"
	controllerResolverScheme          = "aegis-control-plane"
	controllerFailoverServiceConfig   = `{"loadBalancingConfig":[{"pick_first":{}}]}`
	controllerResolverAddressQueryKey = "addr"
)

var registerControllerResolverOnce sync.Once

func DialController(ctx context.Context, controllerAddr string, cfg ClientConfig, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	addresses := EffectiveControllerAddresses(controllerAddr)
	if len(addresses) == 0 {
		return nil, errors.New("controller address is required")
	}
	registerControllerResolverOnce.Do(func() {
		resolver.Register(controllerResolverBuilder{})
	})
	dialOptions, err := ClientDialOptions(cfg)
	if err != nil {
		return nil, err
	}
	dialOptions = append(dialOptions, grpc.WithDefaultServiceConfig(controllerFailoverServiceConfig))
	dialOptions = append(dialOptions, opts...)
	return grpc.DialContext(ctx, controllerTarget(addresses), dialOptions...)
}

func EffectiveControllerAddresses(controllerAddr string) []string {
	addresses := splitControllerAddresses(controllerAddr)
	addresses = append(addresses, splitControllerAddresses(os.Getenv(ControllerAddressesEnv))...)
	return dedupeControllerAddresses(addresses)
}

func controllerTarget(addresses []string) string {
	values := url.Values{}
	for _, address := range addresses {
		values.Add(controllerResolverAddressQueryKey, address)
	}
	return (&url.URL{Scheme: controllerResolverScheme, Path: "/", RawQuery: values.Encode()}).String()
}

type controllerResolverBuilder struct{}

func (controllerResolverBuilder) Scheme() string { return controllerResolverScheme }

func (controllerResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	addresses := dedupeControllerAddresses(target.URL.Query()[controllerResolverAddressQueryKey])
	if len(addresses) == 0 {
		return nil, errors.New("controller resolver target has no addresses")
	}
	resolved := make([]resolver.Address, 0, len(addresses))
	for _, address := range addresses {
		resolved = append(resolved, resolver.Address{Addr: address})
	}
	if err := cc.UpdateState(resolver.State{Addresses: resolved}); err != nil {
		return nil, err
	}
	return controllerResolver{}, nil
}

type controllerResolver struct{}

func (controllerResolver) ResolveNow(resolver.ResolveNowOptions) {}

func (controllerResolver) Close() {}

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

func dedupeControllerAddresses(addresses []string) []string {
	out := make([]string, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		out = append(out, address)
	}
	return out
}

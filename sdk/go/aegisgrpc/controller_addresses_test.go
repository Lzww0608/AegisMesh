package aegisgrpc

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

// TestEffectiveControllerAddressesCombinesAndDedupes locks the effective controller addresses combines and dedupes contract so future changes do not regress it.
func TestEffectiveControllerAddressesCombinesAndDedupes(t *testing.T) {
	t.Setenv(controllerAddressesEnv, "127.0.0.1:9002,127.0.0.1:9001")
	got := effectiveControllerAddresses("127.0.0.1:9000,127.0.0.1:9001", DialOptions{
		ControllerAddrs: []string{"127.0.0.1:9003", "127.0.0.1:9000"},
	})
	want := []string{"127.0.0.1:9000", "127.0.0.1:9001", "127.0.0.1:9003", "127.0.0.1:9002"}
	if len(got) != len(want) {
		t.Fatalf("controller addresses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("controller address[%d] = %q, want %q; all=%+v", i, got[i], want[i], got)
		}
	}
}

// TestControllerControlPlaneConfigUsesOrderedFailover locks the controller control plane config uses ordered failover contract so future changes do not regress it.
func TestControllerControlPlaneConfigUsesOrderedFailover(t *testing.T) {
	if !strings.Contains(controllerFailoverConfig, "pick_first") || strings.Contains(controllerFailoverConfig, "round_robin") {
		t.Fatalf("control-plane service config should use ordered failover, got %s", controllerFailoverConfig)
	}
}

// TestControllerResolverBuildsStaticAddressSet locks the controller resolver builds static address set contract so future changes do not regress it.
func TestControllerResolverBuildsStaticAddressSet(t *testing.T) {
	id := registerControllerAddresses([]string{"127.0.0.1:9000", "127.0.0.1:9001"})
	defer unregisterControllerAddresses(id)

	targetURL, err := url.Parse(controllerTargetForAddressesID(id))
	if err != nil {
		t.Fatalf("parse controller target: %v", err)
	}
	cc := &recordingControllerClientConn{}
	res, err := newControllerResolverBuilder().Build(resolver.Target{URL: *targetURL}, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("build controller resolver: %v", err)
	}
	defer res.Close()

	if len(cc.state.Addresses) != 2 {
		t.Fatalf("expected two controller addresses, got %+v", cc.state.Addresses)
	}
	if cc.state.Addresses[0].Addr != "127.0.0.1:9000" || cc.state.Addresses[1].Addr != "127.0.0.1:9001" {
		t.Fatalf("unexpected controller addresses: %+v", cc.state.Addresses)
	}
}

// TestTargetForServiceCarriesControllerAddressesID locks the target for service carries controller addresses id contract so future changes do not regress it.
func TestTargetForServiceCarriesControllerAddressesID(t *testing.T) {
	addressesID := registerControllerAddresses([]string{"127.0.0.1:9000", "127.0.0.1:9001"})
	defer unregisterControllerAddresses(addressesID)

	target := targetForServiceWithControlPlaneConfig("127.0.0.1:9000", "user-service", "pool-1", "sec-1", addressesID)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if got := parsed.Query().Get(controllerAddressesTargetKey); got != addressesID {
		t.Fatalf("expected controller addresses id %q, got %q", addressesID, got)
	}
}

// TestRegistryResolverFailsClosedWhenSecurityIDIsMissing locks the registry resolver fails closed when security id is missing contract so future changes do not regress it.
func TestRegistryResolverFailsClosedWhenSecurityIDIsMissing(t *testing.T) {
	addressesID := registerControllerAddresses([]string{"127.0.0.1:9000"})
	defer unregisterControllerAddresses(addressesID)
	target := targetForServiceWithControlPlaneConfig("127.0.0.1:9000", "user-service", "", "missing-security", addressesID)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	_, err = newRegistryResolverBuilder().Build(resolver.Target{URL: *parsed}, &recordingControllerClientConn{}, resolver.BuildOptions{})
	if err == nil {
		t.Fatalf("expected missing explicit security id to fail closed")
	}
	if got := loadControllerAddresses(addressesID, ""); len(got) != 1 || got[0] != "127.0.0.1:9000" {
		t.Fatalf("resolver build failure deleted explicit controller address id: %+v", got)
	}
}

// TestDialServiceCleansControllerAddressesAfterEarlyError locks the dial service cleans controller addresses after early error contract so future changes do not regress it.
func TestDialServiceCleansControllerAddressesAfterEarlyError(t *testing.T) {
	before := countControllerAddressRegistrations()
	_, err := DialServiceFromWithOptions(context.Background(), "test", "127.0.0.1:9000,127.0.0.1:9001", "user-service", DialOptions{
		RoutingPolicy:    "unsupported",
		DisablePolicy:    true,
		DisableTelemetry: true,
	})
	if err == nil {
		t.Fatalf("expected unsupported routing policy error")
	}
	if after := countControllerAddressRegistrations(); after != before {
		t.Fatalf("controller address registry leaked: before=%d after=%d", before, after)
	}
}

// TestDialServiceCleansRegistrationsAfterDialError locks the dial service cleans registrations after dial error contract so future changes do not regress it.
func TestDialServiceCleansRegistrationsAfterDialError(t *testing.T) {
	beforeAddresses := countControllerAddressRegistrations()
	beforeSecurity := countControllerSecurityRegistrations()
	beforeLimiters := countAdaptiveLimiterPoolRegistrations()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := DialServiceFromWithOptions(ctx, "test", "127.0.0.1:9000", "user-service", DialOptions{
		DisablePolicy:    true,
		DisableTelemetry: true,
	}, grpc.WithBlock())
	if err == nil {
		t.Fatalf("expected blocked dial to fail")
	}
	if after := countControllerAddressRegistrations(); after != beforeAddresses {
		t.Fatalf("controller address registry leaked: before=%d after=%d", beforeAddresses, after)
	}
	if after := countControllerSecurityRegistrations(); after != beforeSecurity {
		t.Fatalf("controller security registry leaked: before=%d after=%d", beforeSecurity, after)
	}
	if after := countAdaptiveLimiterPoolRegistrations(); after != beforeLimiters {
		t.Fatalf("adaptive limiter registry leaked: before=%d after=%d", beforeLimiters, after)
	}
}

// countControllerAddressRegistrations provides the shared count controller address registrations helper for resolver, picker, and reporter state.
func countControllerAddressRegistrations() int {
	count := 0
	controllerAddressesByID.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// countControllerSecurityRegistrations provides the shared count controller security registrations helper for resolver, picker, and reporter state.
func countControllerSecurityRegistrations() int {
	count := 0
	controllerSecurityByID.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// countAdaptiveLimiterPoolRegistrations provides the shared count adaptive limiter pool registrations helper for resolver, picker, and reporter state.
func countAdaptiveLimiterPoolRegistrations() int {
	count := 0
	adaptiveLimiterPools.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// recordingControllerClientConn carries recording controller client conn state for resolver, picker, and reporter state.
type recordingControllerClientConn struct {
	state resolver.State
}

// UpdateState records fake controller health updates without running the production state machine.
func (c *recordingControllerClientConn) UpdateState(state resolver.State) error {
	c.state = state
	return nil
}

// ReportError records controller resolver errors for duplicate-registration tests.
func (c *recordingControllerClientConn) ReportError(error) {}

// NewAddress initializes address with package defaults for this package's call path.
func (c *recordingControllerClientConn) NewAddress([]resolver.Address) {}

// ParseServiceConfig decodes service config input into the package's typed representation.
func (c *recordingControllerClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

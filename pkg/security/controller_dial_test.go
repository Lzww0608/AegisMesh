package security

import (
	"net/url"
	"testing"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

// TestEffectiveControllerAddressesCombinesEnvAndDedupes locks the effective controller addresses combines env and dedupes contract so future changes do not regress it.
func TestEffectiveControllerAddressesCombinesEnvAndDedupes(t *testing.T) {
	t.Setenv(ControllerAddressesEnv, "127.0.0.1:9001,127.0.0.1:9002")
	got := EffectiveControllerAddresses("127.0.0.1:9000,127.0.0.1:9001")
	want := []string{"127.0.0.1:9000", "127.0.0.1:9001", "127.0.0.1:9002"}
	if len(got) != len(want) {
		t.Fatalf("addresses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("address[%d] = %q, want %q; all=%+v", i, got[i], want[i], got)
		}
	}
}

// TestControllerTargetCarriesAddressSet locks the controller target carries address set contract so future changes do not regress it.
func TestControllerTargetCarriesAddressSet(t *testing.T) {
	target := controllerTarget([]string{"127.0.0.1:9000", "127.0.0.1:9001"})
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	cc := &recordingSecurityResolverClientConn{}
	res, err := controllerResolverBuilder{}.Build(resolver.Target{URL: *parsed}, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	defer res.Close()
	if len(cc.state.Addresses) != 2 || cc.state.Addresses[0].Addr != "127.0.0.1:9000" || cc.state.Addresses[1].Addr != "127.0.0.1:9001" {
		t.Fatalf("unexpected resolved addresses: %+v", cc.state.Addresses)
	}
}

// TestControllerResolverRejectsEmptyAddressSet locks the controller resolver rejects empty address set contract so future changes do not regress it.
func TestControllerResolverRejectsEmptyAddressSet(t *testing.T) {
	parsed, err := url.Parse(controllerTarget(nil))
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	_, err = controllerResolverBuilder{}.Build(resolver.Target{URL: *parsed}, &recordingSecurityResolverClientConn{}, resolver.BuildOptions{})
	if err == nil {
		t.Fatalf("expected empty controller target to be rejected")
	}
}

// recordingSecurityResolverClientConn carries recording security resolver client conn state for authorization checks.
type recordingSecurityResolverClientConn struct {
	state resolver.State
}

// UpdateState records fake controller health updates without running the production state machine.
func (c *recordingSecurityResolverClientConn) UpdateState(state resolver.State) error {
	c.state = state
	return nil
}

// ReportError records resolver errors while security tests inspect controller dial setup.
func (c *recordingSecurityResolverClientConn) ReportError(error) {}

// NewAddress initializes address with package defaults for this package's call path.
func (c *recordingSecurityResolverClientConn) NewAddress([]resolver.Address) {}

// ParseServiceConfig decodes service config input into the package's typed representation.
func (c *recordingSecurityResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

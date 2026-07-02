package security

import (
	"net/url"
	"testing"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

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

type recordingSecurityResolverClientConn struct {
	state resolver.State
}

func (c *recordingSecurityResolverClientConn) UpdateState(state resolver.State) error {
	c.state = state
	return nil
}

func (c *recordingSecurityResolverClientConn) ReportError(error) {}

func (c *recordingSecurityResolverClientConn) NewAddress([]resolver.Address) {}

func (c *recordingSecurityResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

package aegisgrpc

import (
	"net/url"
	"testing"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc/resolver"
)

func TestTargetForServiceBuildsAegisTarget(t *testing.T) {
	got := TargetForService("127.0.0.1:9000", "user-service")
	if got != "aegis://127.0.0.1:9000/user-service" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestParseTargetExtractsControllerAndService(t *testing.T) {
	u, err := url.Parse("aegis://127.0.0.1:9000/user-service")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	controller, service, err := parseTarget(resolver.Target{URL: *u})
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if controller != "127.0.0.1:9000" {
		t.Fatalf("expected controller address, got %s", controller)
	}
	if service != "user-service" {
		t.Fatalf("expected service name, got %s", service)
	}
}

func TestInstancesToAddressesKeepsOnlyHealthyInstances(t *testing.T) {
	got := instancesToAddresses([]*aegisv1.ServiceInstance{
		{Id: "user-a", Address: "127.0.0.1:7001", Status: "HEALTHY"},
		{Id: "user-b", Address: "127.0.0.1:7002", Status: "DEAD"},
		{Id: "user-c", Address: "127.0.0.1:7003"},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 routable addresses, got %d", len(got))
	}
	if got[0].Addr != "127.0.0.1:7001" || got[1].Addr != "127.0.0.1:7003" {
		t.Fatalf("unexpected addresses: %+v", got)
	}
}

func TestInstancesToAddressesAttachesAegisAttributes(t *testing.T) {
	got := instancesToAddresses([]*aegisv1.ServiceInstance{
		{Id: "user-a", Address: "127.0.0.1:7001", Status: "DEGRADED", SlowScore: 1.75},
	})

	if len(got) != 1 {
		t.Fatalf("expected one address, got %d", len(got))
	}
	if instanceIDFromAttributes(got[0].Attributes) != "user-a" {
		t.Fatalf("expected instance id attribute user-a")
	}
	if endpointStatusFromAttributes(got[0].Attributes) != "DEGRADED" {
		t.Fatalf("expected status attribute DEGRADED")
	}
	if slowScoreFromAttributes(got[0].Attributes) != 1.75 {
		t.Fatalf("expected slow score attribute 1.75")
	}
}

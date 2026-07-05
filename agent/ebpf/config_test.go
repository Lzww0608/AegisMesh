package ebpf

import "testing"

// TestParseEndpointMap locks the parse endpoint map contract so future changes do not regress it.
func TestParseEndpointMap(t *testing.T) {
	got, err := ParseEndpointMap("10.0.0.2:7001=user-service/user-a,10.0.0.3:7101=order-service/order-a")
	if err != nil {
		t.Fatalf("parse endpoint map: %v", err)
	}
	if got["10.0.0.2:7001"].Service != "user-service" || got["10.0.0.2:7001"].InstanceID != "user-a" {
		t.Fatalf("unexpected user mapping: %+v", got)
	}
	if got["10.0.0.3:7101"].Address != "10.0.0.3:7101" {
		t.Fatalf("expected address to be preserved: %+v", got)
	}
}

// TestParseEndpointMapRejectsMalformedEntry locks the parse endpoint map rejects malformed entry contract so future changes do not regress it.
func TestParseEndpointMapRejectsMalformedEntry(t *testing.T) {
	if _, err := ParseEndpointMap("10.0.0.2:7001=user-service"); err == nil {
		t.Fatalf("expected malformed mapping to fail")
	}
}

package ebpf

import "testing"

// TestPackAndFormatEndpoint locks the pack and format endpoint contract so future changes do not regress it.
func TestPackAndFormatEndpoint(t *testing.T) {
	key := packEndpoint(0x0200000a, 7001)
	if FormatEndpoint(key) != "10.0.0.2:7001" {
		t.Fatalf("unexpected formatted endpoint: %s", FormatEndpoint(key))
	}
}

// TestParseEndpointKey locks the parse endpoint key contract so future changes do not regress it.
func TestParseEndpointKey(t *testing.T) {
	key, err := ParseEndpointKey("10.0.0.3:7002")
	if err != nil {
		t.Fatalf("parse endpoint key: %v", err)
	}
	if key != packEndpoint(0x0300000a, 7002) {
		t.Fatalf("unexpected parsed key: %#x", key)
	}
	if FormatEndpoint(key) != "10.0.0.3:7002" {
		t.Fatalf("unexpected round-trip endpoint: %s", FormatEndpoint(key))
	}
}

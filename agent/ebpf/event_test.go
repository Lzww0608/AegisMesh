package ebpf

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// TestDecodeRawTCPRetransmitEvent locks the decode raw tcp retransmit event contract so future changes do not regress it.
func TestDecodeRawTCPRetransmitEvent(t *testing.T) {
	raw := testRawTCPEvent{
		TimestampNS: uint64(time.Second),
		PID:         4242,
		Type:        uint32(EventTypeRetransmit),
		Dport:       7001,
		DaddrV4:     0x0200000a,
	}
	copy(raw.Comm[:], "demo-user")

	event, err := DecodeRawTCPEvent(encodeTestRawEvent(t, raw))
	if err != nil {
		t.Fatalf("decode raw event: %v", err)
	}
	if event.Type != EventTypeRetransmit {
		t.Fatalf("expected retransmit event type, got %s", event.Type)
	}
	if event.RemoteAddress() != "10.0.0.2:7001" {
		t.Fatalf("expected remote address 10.0.0.2:7001, got %s", event.RemoteAddress())
	}
	if event.RemoteKey != packEndpoint(0x0200000a, 7001) {
		t.Fatalf("unexpected remote key: %#x", event.RemoteKey)
	}
	if event.Retransmits != 1 || event.ConnectErrors != 0 {
		t.Fatalf("unexpected counters: %+v", event)
	}
	if event.PID != 4242 || event.CommString() != "demo-user" {
		t.Fatalf("unexpected process identity: pid=%d comm=%q", event.PID, event.CommString())
	}
}

// TestDecodeRawTCPConnectErrorEvent locks the decode raw tcp connect error event contract so future changes do not regress it.
func TestDecodeRawTCPConnectErrorEvent(t *testing.T) {
	raw := testRawTCPEvent{
		TimestampNS:      uint64(2 * time.Second),
		Type:             uint32(EventTypeConnect),
		Dport:            7002,
		DaddrV4:          0x0300000a,
		Ret:              -111,
		ConnectLatencyNS: uint64(25 * time.Millisecond),
	}

	event, err := DecodeRawTCPEvent(encodeTestRawEvent(t, raw))
	if err != nil {
		t.Fatalf("decode raw event: %v", err)
	}
	if event.Type != EventTypeConnect {
		t.Fatalf("expected connect event type, got %s", event.Type)
	}
	if event.RemoteAddress() != "10.0.0.3:7002" {
		t.Fatalf("expected remote address 10.0.0.3:7002, got %s", event.RemoteAddress())
	}
	if event.ConnectErrors != 1 {
		t.Fatalf("expected connect error counter 1, got %+v", event)
	}
	if event.ConnectLatency != 25*time.Millisecond {
		t.Fatalf("expected 25ms connect latency, got %s", event.ConnectLatency)
	}
}

// TestDecodeRawTCPEventRejectsShortSample locks the decode raw tcp event rejects short sample contract so future changes do not regress it.
func TestDecodeRawTCPEventRejectsShortSample(t *testing.T) {
	_, err := DecodeRawTCPEvent([]byte{1, 2, 3})
	if !errors.Is(err, ErrShortSample) {
		t.Fatalf("expected ErrShortSample, got %v", err)
	}
}

// testRawTCPEvent mirrors the binary TCP event layout used by decode fixtures.
type testRawTCPEvent struct {
	TimestampNS      uint64
	PID              uint32
	Type             uint32
	Family           uint16
	Sport            uint16
	Dport            uint16
	Pad              uint16
	Ret              int32
	SaddrV4          uint32
	DaddrV4          uint32
	ConnectLatencyNS uint64
	Comm             [16]byte
}

// encodeTestRawEvent keeps encode test raw event rules consistent for the eBPF telemetry path.
func encodeTestRawEvent(t *testing.T, event testRawTCPEvent) []byte {
	t.Helper()
	buf := make([]byte, rawTCPEventSize)
	binary.LittleEndian.PutUint64(buf[offTimestampNS:], event.TimestampNS)
	binary.LittleEndian.PutUint32(buf[offPID:], event.PID)
	binary.LittleEndian.PutUint32(buf[offType:], event.Type)
	binary.LittleEndian.PutUint16(buf[offSport:], event.Sport)
	binary.LittleEndian.PutUint16(buf[offDport:], event.Dport)
	binary.LittleEndian.PutUint32(buf[offRet:], uint32(event.Ret))
	binary.LittleEndian.PutUint32(buf[offSaddrV4:], event.SaddrV4)
	binary.LittleEndian.PutUint32(buf[offDaddrV4:], event.DaddrV4)
	binary.LittleEndian.PutUint64(buf[offConnectLatencyNS:], event.ConnectLatencyNS)
	copy(buf[offComm:], event.Comm[:])
	return buf
}

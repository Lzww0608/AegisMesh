package ebpf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestDecodeRawTCPRetransmitEvent(t *testing.T) {
	raw := rawTCPEvent{
		TimestampNS: uint64(time.Second),
		PID:         4242,
		Type:        uint32(EventTypeRetransmit),
		Dport:       7001,
		DaddrV4:     0x0200000a,
	}
	copy(raw.Comm[:], "demo-user")

	event, err := DecodeRawTCPEvent(encodeRawEvent(t, raw))
	if err != nil {
		t.Fatalf("decode raw event: %v", err)
	}
	if event.Type != EventTypeRetransmit {
		t.Fatalf("expected retransmit event type, got %s", event.Type)
	}
	if event.RemoteAddr != "10.0.0.2:7001" {
		t.Fatalf("expected remote address 10.0.0.2:7001, got %s", event.RemoteAddr)
	}
	if event.Retransmits != 1 || event.ConnectErrors != 0 {
		t.Fatalf("unexpected counters: %+v", event)
	}
	if event.PID != 4242 || event.Comm != "demo-user" {
		t.Fatalf("unexpected process identity: %+v", event)
	}
}

func TestDecodeRawTCPConnectErrorEvent(t *testing.T) {
	raw := rawTCPEvent{
		TimestampNS:      uint64(2 * time.Second),
		Type:             uint32(EventTypeConnect),
		Dport:            7002,
		DaddrV4:          0x0300000a,
		Ret:              -111,
		ConnectLatencyNS: uint64(25 * time.Millisecond),
	}

	event, err := DecodeRawTCPEvent(encodeRawEvent(t, raw))
	if err != nil {
		t.Fatalf("decode raw event: %v", err)
	}
	if event.Type != EventTypeConnect {
		t.Fatalf("expected connect event type, got %s", event.Type)
	}
	if event.RemoteAddr != "10.0.0.3:7002" {
		t.Fatalf("expected remote address 10.0.0.3:7002, got %s", event.RemoteAddr)
	}
	if event.ConnectErrors != 1 {
		t.Fatalf("expected connect error counter 1, got %+v", event)
	}
	if event.ConnectLatency != 25*time.Millisecond {
		t.Fatalf("expected 25ms connect latency, got %s", event.ConnectLatency)
	}
}

func TestDecodeRawTCPEventRejectsShortSample(t *testing.T) {
	_, err := DecodeRawTCPEvent([]byte{1, 2, 3})
	if !errors.Is(err, ErrShortSample) {
		t.Fatalf("expected ErrShortSample, got %v", err)
	}
}

func encodeRawEvent(t *testing.T, event rawTCPEvent) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, event); err != nil {
		t.Fatalf("encode raw event: %v", err)
	}
	return buf.Bytes()
}

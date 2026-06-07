package ebpf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var ErrShortSample = errors.New("short eBPF ringbuf sample")

type EventType uint32

const (
	EventTypeUnspecified EventType = 0
	EventTypeRetransmit  EventType = 1
	EventTypeConnect     EventType = 2
)

func (t EventType) String() string {
	switch t {
	case EventTypeRetransmit:
		return "retransmit"
	case EventTypeConnect:
		return "connect"
	default:
		return "unspecified"
	}
}

type rawTCPEvent struct {
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

func DecodeRawTCPEvent(sample []byte) (TCPEvent, error) {
	if len(sample) < binary.Size(rawTCPEvent{}) {
		return TCPEvent{}, ErrShortSample
	}

	var raw rawTCPEvent
	if err := binary.Read(bytes.NewReader(sample), binary.LittleEndian, &raw); err != nil {
		return TCPEvent{}, err
	}

	eventType := EventType(raw.Type)
	event := TCPEvent{
		Type:           eventType,
		RemoteAddr:     endpointString(raw.DaddrV4, raw.Dport),
		LocalAddr:      endpointString(raw.SaddrV4, raw.Sport),
		PID:            raw.PID,
		Comm:           commString(raw.Comm),
		ConnectLatency: time.Duration(raw.ConnectLatencyNS),
		ObservedAt:     time.Unix(0, int64(raw.TimestampNS)),
	}
	switch eventType {
	case EventTypeRetransmit:
		event.Retransmits = 1
	case EventTypeConnect:
		if raw.Ret != 0 {
			event.ConnectErrors = 1
		}
	}
	return event, nil
}

func endpointString(addrV4 uint32, port uint16) string {
	if addrV4 == 0 || port == 0 {
		return ""
	}
	ip := net.IPv4(byte(addrV4), byte(addrV4>>8), byte(addrV4>>16), byte(addrV4>>24))
	return fmt.Sprintf("%s:%d", ip.String(), port)
}

func commString(comm [16]byte) string {
	raw := string(comm[:])
	if idx := strings.IndexByte(raw, 0); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}

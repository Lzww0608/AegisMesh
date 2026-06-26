package ebpf

import (
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

var ErrShortSample = errors.New("short eBPF ringbuf sample")

const rawTCPEventSize = 60

const (
	offTimestampNS      = 0
	offPID              = 8
	offType             = 12
	offSport            = 18
	offDport            = 20
	offRet              = 24
	offSaddrV4          = 28
	offDaddrV4          = 32
	offConnectLatencyNS = 36
	offComm             = 44
)

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

func DecodeRawTCPEvent(sample []byte) (TCPEvent, error) {
	if len(sample) < rawTCPEventSize {
		return TCPEvent{}, ErrShortSample
	}

	eventType := EventType(binary.LittleEndian.Uint32(sample[offType:]))
	daddrV4 := binary.LittleEndian.Uint32(sample[offDaddrV4:])
	dport := binary.LittleEndian.Uint16(sample[offDport:])
	saddrV4 := binary.LittleEndian.Uint32(sample[offSaddrV4:])
	sport := binary.LittleEndian.Uint16(sample[offSport:])
	ret := int32(binary.LittleEndian.Uint32(sample[offRet:]))

	event := TCPEvent{
		Type:           eventType,
		RemoteKey:      packEndpoint(daddrV4, dport),
		LocalKey:       packEndpoint(saddrV4, sport),
		PID:            binary.LittleEndian.Uint32(sample[offPID:]),
		ConnectLatency: time.Duration(binary.LittleEndian.Uint64(sample[offConnectLatencyNS:])),
		ObservedAt:     time.Unix(0, int64(binary.LittleEndian.Uint64(sample[offTimestampNS:]))),
	}
	copy(event.Comm[:], sample[offComm:offComm+16])

	switch eventType {
	case EventTypeRetransmit:
		event.Retransmits = 1
	case EventTypeConnect:
		if ret != 0 {
			event.ConnectErrors = 1
		}
	}
	return event, nil
}

func commString(comm [16]byte) string {
	raw := string(comm[:])
	if idx := strings.IndexByte(raw, 0); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}

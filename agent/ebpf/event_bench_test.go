package ebpf

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

var benchmarkTCPEvent TCPEvent

func BenchmarkDecodeRawTCPEvent(b *testing.B) {
	raw := rawTCPEvent{
		TimestampNS:      uint64(time.Second),
		PID:              4242,
		Type:             uint32(EventTypeConnect),
		Sport:            50000,
		Dport:            7001,
		SaddrV4:          0x0100000a,
		DaddrV4:          0x0200000a,
		ConnectLatencyNS: uint64(25 * time.Millisecond),
	}
	copy(raw.Comm[:], "bench-client")
	sample := encodeBenchmarkRawEvent(b, raw)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkTCPEvent, err = DecodeRawTCPEvent(sample)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func encodeBenchmarkRawEvent(b *testing.B, event rawTCPEvent) []byte {
	b.Helper()
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, event); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}

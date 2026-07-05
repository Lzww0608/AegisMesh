package ebpf

import (
	"os"
	"strings"
	"testing"
)

// TestBPFProgramDeclaresRequiredHooksAndMaps locks the bpf program declares required hooks and maps contract so future changes do not regress it.
func TestBPFProgramDeclaresRequiredHooksAndMaps(t *testing.T) {
	source, err := os.ReadFile("bpf/tcp_metrics.bpf.c")
	if err != nil {
		t.Fatalf("read bpf source: %v", err)
	}
	text := string(source)
	required := []string{
		`SEC("kprobe/tcp_retransmit_skb")`,
		`SEC("kprobe/tcp_v4_connect")`,
		`SEC("kretprobe/tcp_v4_connect")`,
		"BPF_MAP_TYPE_RINGBUF",
		"BPF_MAP_TYPE_HASH",
		"connect_latency_ns",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("expected BPF source to contain %q", want)
		}
	}
}

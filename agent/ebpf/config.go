package ebpf

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const defaultReportInterval = 5 * time.Second

// DefaultObjectPath keeps default object path rules consistent for the eBPF telemetry path.
func DefaultObjectPath() string {
	return filepath.Join("agent", "ebpf", "bpf", "tcp_metrics.bpf.o")
}

// ParseEndpointMap decodes endpoint map input into the package's typed representation.
func ParseEndpointMap(raw string) (map[string]EndpointRef, error) {
	out := make(map[string]EndpointRef)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid endpoint mapping %q", entry)
		}
		address := strings.TrimSpace(parts[0])
		serviceParts := strings.Split(strings.TrimSpace(parts[1]), "/")
		if address == "" || len(serviceParts) != 2 || serviceParts[0] == "" || serviceParts[1] == "" {
			return nil, fmt.Errorf("invalid endpoint mapping %q", entry)
		}
		out[address] = EndpointRef{
			Service:    serviceParts[0],
			InstanceID: serviceParts[1],
			Address:    address,
		}
	}
	return out, nil
}

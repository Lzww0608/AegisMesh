package ebpf

import (
	"fmt"
	"net"
	"strconv"
)

// EndpointKey names the endpoint key values accepted by the eBPF telemetry path.
type EndpointKey uint64

// packEndpoint keeps pack endpoint rules consistent for the eBPF telemetry path.
func packEndpoint(ipv4 uint32, port uint16) EndpointKey {
	if ipv4 == 0 || port == 0 {
		return 0
	}
	return EndpointKey(uint64(ipv4)<<16 | uint64(port))
}

// FormatEndpoint keeps format endpoint rules consistent for the eBPF telemetry path.
func FormatEndpoint(key EndpointKey) string {
	if key == 0 {
		return ""
	}
	v := uint64(key)
	port := uint16(v)
	ipv4 := uint32(v >> 16)
	ip := net.IPv4(byte(ipv4), byte(ipv4>>8), byte(ipv4>>16), byte(ipv4>>24))
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
}

// ParseEndpointKey decodes endpoint key input into the package's typed representation.
func ParseEndpointKey(addr string) (EndpointKey, error) {
	if addr == "" {
		return 0, nil
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse endpoint %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse endpoint port %q: %w", portStr, err)
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("parse endpoint port %q: out of range", portStr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return 0, fmt.Errorf("parse endpoint host %q: invalid IP", host)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, fmt.Errorf("parse endpoint host %q: IPv4 required", host)
	}
	ipv4 := uint32(ip4[0]) | uint32(ip4[1])<<8 | uint32(ip4[2])<<16 | uint32(ip4[3])<<24
	return packEndpoint(ipv4, uint16(port)), nil
}

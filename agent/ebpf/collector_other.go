//go:build !linux

package ebpf

func NewCollector(Config) (Collector, error) {
	return &unsupportedCollector{events: make(chan TCPEvent)}, ErrUnsupportedPlatform
}

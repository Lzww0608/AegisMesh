//go:build !linux

package ebpf

// NewCollector initializes collector with package defaults for this package's call path.
func NewCollector(Config) (Collector, error) {
	return &unsupportedCollector{events: make(chan TCPEvent)}, ErrUnsupportedPlatform
}

package ebpf

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrUnsupportedPlatform = errors.New("eBPF collector is only available on Linux")

// Config maps Linux TCP events to AegisMesh endpoint identities.
type Config struct {
	Interface      string
	ObjectPath     string
	ControllerAddr string
	EndpointMap    map[string]EndpointRef
}

// Collector abstracts the Linux eBPF event source for tests and non-Linux builds.
type Collector interface {
	Start() error
	Stop() error
	Events() <-chan TCPEvent
}

// EndpointRef carries endpoint ref state for the eBPF telemetry path.
type EndpointRef struct {
	Service    string
	InstanceID string
	Address    string
}

// TCPEvent is the normalized userspace view of one BPF TCP signal.
type TCPEvent struct {
	RemoteKey      EndpointKey
	LocalKey       EndpointKey
	Type           EventType
	RemoteAddr     string
	LocalAddr      string
	PID            uint32
	Comm           [16]byte
	Retransmits    int64
	ConnectErrors  int64
	ConnectLatency time.Duration
	ObservedAt     time.Time
}

// RemoteAddress returns remote address data for TCPEvent callers without handing out mutable receiver state.
func (e TCPEvent) RemoteAddress() string {
	if e.RemoteAddr != "" {
		return e.RemoteAddr
	}
	return FormatEndpoint(e.RemoteKey)
}

// LocalAddress returns local address data for TCPEvent callers without handing out mutable receiver state.
func (e TCPEvent) LocalAddress() string {
	if e.LocalAddr != "" {
		return e.LocalAddr
	}
	return FormatEndpoint(e.LocalKey)
}

// CommString returns comm string data for TCPEvent callers without handing out mutable receiver state.
func (e TCPEvent) CommString() string {
	return commString(e.Comm)
}

// remoteEndpointKey returns remote endpoint key data for TCPEvent callers without handing out mutable receiver state.
func (e TCPEvent) remoteEndpointKey() EndpointKey {
	if e.RemoteKey != 0 {
		return e.RemoteKey
	}
	if e.RemoteAddr == "" {
		return 0
	}
	key, err := ParseEndpointKey(e.RemoteAddr)
	if err != nil {
		return 0
	}
	return key
}

// NetworkSample is one reporting-window aggregate for a known endpoint.
type NetworkSample struct {
	Service        string
	InstanceID     string
	Address        string
	TCPRetransmit  int64
	ConnectError   int64
	ConnectLatency time.Duration
	WindowStart    time.Time
	WindowEnd      time.Time
}

// Aggregator folds raw TCP events into per-endpoint reporting windows.
type Aggregator struct {
	mu        sync.Mutex
	endpoints map[EndpointKey]EndpointRef
	rows      map[EndpointKey]*networkRow
}

// networkRow carries network row state for the eBPF telemetry path.
type networkRow struct {
	ref            EndpointRef
	retransmits    int64
	connectErrors  int64
	connectLatency time.Duration
	windowStart    time.Time
	windowEnd      time.Time
}

// NewAggregator initializes aggregator with package defaults for this package's call path.
func NewAggregator(endpoints map[string]EndpointRef) *Aggregator {
	refs := make(map[EndpointKey]EndpointRef, len(endpoints))
	for addr, ref := range endpoints {
		key, err := ParseEndpointKey(addr)
		if err != nil {
			continue
		}
		if ref.Address == "" {
			ref.Address = addr
		}
		refs[key] = ref
	}
	return &Aggregator{
		endpoints: refs,
		rows:      make(map[EndpointKey]*networkRow),
	}
}

// Observe folds one TCP event into the current endpoint window.
func (a *Aggregator) Observe(event TCPEvent) {
	key := event.remoteEndpointKey()
	if key == 0 {
		return
	}

	// Aggregation is serialized because events can arrive while the reporter snapshots.
	a.mu.Lock()
	defer a.mu.Unlock()

	ref, ok := a.endpoints[key]
	if !ok {
		addr := FormatEndpoint(key)
		ref = EndpointRef{Address: addr, InstanceID: addr}
	}
	row := a.rows[key]
	if row == nil {
		row = &networkRow{ref: ref, windowStart: event.ObservedAt}
		a.rows[key] = row
	}
	if row.windowStart.IsZero() {
		row.windowStart = event.ObservedAt
	}
	row.windowEnd = event.ObservedAt
	row.retransmits += event.Retransmits
	row.connectErrors += event.ConnectErrors
	row.connectLatency += event.ConnectLatency
}

// SnapshotAndReset returns an immutable snapshot of the current snapshot and reset state.
func (a *Aggregator) SnapshotAndReset() []NetworkSample {
	// Aggregation is serialized because events can arrive while the reporter snapshots.
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]NetworkSample, 0, len(a.rows))
	for _, row := range a.rows {
		out = append(out, NetworkSample{
			Service:        row.ref.Service,
			InstanceID:     row.ref.InstanceID,
			Address:        row.ref.Address,
			TCPRetransmit:  row.retransmits,
			ConnectError:   row.connectErrors,
			ConnectLatency: row.connectLatency,
			WindowStart:    row.windowStart,
			WindowEnd:      row.windowEnd,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Address < out[j].Address
	})
	// Reset after snapshot gives each exported sample a closed time window.
	a.rows = make(map[EndpointKey]*networkRow)
	return out
}

// unsupportedCollector preserves the Collector API on platforms without eBPF support.
type unsupportedCollector struct {
	events chan TCPEvent
}

// Start begins collection and binds the collector lifetime to its owned resources.
func (c *unsupportedCollector) Start() error {
	return ErrUnsupportedPlatform
}

// Stop releases collector resources and makes repeated shutdown calls harmless.
func (c *unsupportedCollector) Stop() error {
	return nil
}

// Events returns events data for unsupportedCollector callers without handing out mutable receiver state.
func (c *unsupportedCollector) Events() <-chan TCPEvent {
	return c.events
}

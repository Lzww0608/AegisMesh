package ebpf

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrUnsupportedPlatform = errors.New("eBPF collector is only available on Linux")

type Config struct {
	Interface      string
	ObjectPath     string
	ControllerAddr string
	EndpointMap    map[string]EndpointRef
}

type Collector interface {
	Start() error
	Stop() error
	Events() <-chan TCPEvent
}

type EndpointRef struct {
	Service    string
	InstanceID string
	Address    string
}

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

func (e TCPEvent) RemoteAddress() string {
	if e.RemoteAddr != "" {
		return e.RemoteAddr
	}
	return FormatEndpoint(e.RemoteKey)
}

func (e TCPEvent) LocalAddress() string {
	if e.LocalAddr != "" {
		return e.LocalAddr
	}
	return FormatEndpoint(e.LocalKey)
}

func (e TCPEvent) CommString() string {
	return commString(e.Comm)
}

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

type Aggregator struct {
	mu        sync.Mutex
	endpoints map[EndpointKey]EndpointRef
	rows      map[EndpointKey]*networkRow
}

type networkRow struct {
	ref            EndpointRef
	retransmits    int64
	connectErrors  int64
	connectLatency time.Duration
	windowStart    time.Time
	windowEnd      time.Time
}

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

func (a *Aggregator) Observe(event TCPEvent) {
	key := event.remoteEndpointKey()
	if key == 0 {
		return
	}

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

func (a *Aggregator) SnapshotAndReset() []NetworkSample {
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
	a.rows = make(map[EndpointKey]*networkRow)
	return out
}

type unsupportedCollector struct {
	events chan TCPEvent
}

func (c *unsupportedCollector) Start() error {
	return ErrUnsupportedPlatform
}

func (c *unsupportedCollector) Stop() error {
	return nil
}

func (c *unsupportedCollector) Events() <-chan TCPEvent {
	return c.events
}

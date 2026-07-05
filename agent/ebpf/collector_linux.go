//go:build linux

package ebpf

import (
	"errors"
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

const eventBufferSize = 1024

// linuxCollector carries linux collector state for the eBPF telemetry path.
type linuxCollector struct {
	cfg Config

	events  chan TCPEvent
	metrics *CollectorMetrics

	mu         sync.Mutex
	collection *ebpf.Collection
	reader     *ringbuf.Reader
	links      []link.Link

	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewCollector initializes collector with package defaults for this package's call path.
func NewCollector(cfg Config) (Collector, error) {
	if cfg.ObjectPath == "" {
		cfg.ObjectPath = DefaultObjectPath()
	}
	return &linuxCollector{
		cfg:     cfg,
		events:  make(chan TCPEvent, eventBufferSize),
		metrics: DefaultCollectorMetrics(),
	}, nil
}

// Start begins collection and binds the collector lifetime to its owned resources.
func (c *linuxCollector) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.collection != nil {
		return nil
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("remove eBPF memlock limit: %w", err)
	}

	spec, err := ebpf.LoadCollectionSpec(c.cfg.ObjectPath)
	if err != nil {
		return fmt.Errorf("load eBPF object spec %q: %w", c.cfg.ObjectPath, err)
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		return fmt.Errorf("load eBPF collection %q: %w", c.cfg.ObjectPath, err)
	}
	c.collection = collection

	if err := c.attachProgramsLocked(); err != nil {
		_ = c.closeLocked()
		return err
	}

	eventsMap := c.collection.Maps["events"]
	if eventsMap == nil {
		_ = c.closeLocked()
		return fmt.Errorf("eBPF object %q does not contain ringbuf map %q", c.cfg.ObjectPath, "events")
	}
	reader, err := ringbuf.NewReader(eventsMap)
	if err != nil {
		_ = c.closeLocked()
		return fmt.Errorf("open eBPF events ringbuf: %w", err)
	}
	c.reader = reader

	c.wg.Add(1)
	go c.readLoop(reader)
	return nil
}

// Stop releases collector resources and makes repeated shutdown calls harmless.
func (c *linuxCollector) Stop() error {
	var err error
	c.stopOnce.Do(func() {
		c.mu.Lock()
		err = c.closeLocked()
		c.mu.Unlock()
		c.wg.Wait()
		close(c.events)
	})
	return err
}

// Events returns events data for linuxCollector callers without handing out mutable receiver state.
func (c *linuxCollector) Events() <-chan TCPEvent {
	return c.events
}

// attachProgramsLocked returns attach programs locked data for linuxCollector callers without handing out mutable receiver state.
func (c *linuxCollector) attachProgramsLocked() error {
	attachPlan := []struct {
		program string
		symbol  string
		ret     bool
	}{
		{program: "aegis_tcp_retransmit", symbol: "tcp_retransmit_skb"},
		{program: "aegis_tcp_v4_connect", symbol: "tcp_v4_connect"},
		{program: "aegis_tcp_v4_connect_ret", symbol: "tcp_v4_connect", ret: true},
	}

	for _, item := range attachPlan {
		program := c.collection.Programs[item.program]
		if program == nil {
			return fmt.Errorf("eBPF object %q does not contain program %q", c.cfg.ObjectPath, item.program)
		}
		var (
			// attached identifies the attached constant used by this package.
			attached link.Link
			err      error
		)
		if item.ret {
			attached, err = link.Kretprobe(item.symbol, program, nil)
		} else {
			attached, err = link.Kprobe(item.symbol, program, nil)
		}
		if err != nil {
			return fmt.Errorf("attach %s to %s: %w", item.program, item.symbol, err)
		}
		c.links = append(c.links, attached)
	}
	return nil
}

// closeLocked closes owned resources and makes repeated calls safe.
func (c *linuxCollector) closeLocked() error {
	var closeErr error
	if c.reader != nil {
		closeErr = errors.Join(closeErr, c.reader.Close())
		c.reader = nil
	}
	for i := len(c.links) - 1; i >= 0; i-- {
		closeErr = errors.Join(closeErr, c.links[i].Close())
	}
	c.links = nil
	if c.collection != nil {
		c.collection.Close()
		c.collection = nil
	}
	return closeErr
}

// readLoop reads read loop data from the supplied input.
func (c *linuxCollector) readLoop(reader *ringbuf.Reader) {
	defer c.wg.Done()
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			continue
		}
		event, err := DecodeRawTCPEvent(record.RawSample)
		if err != nil {
			continue
		}
		select {
		case c.events <- event:
		default:
			c.metrics.IncDropped("channel_full")
		}
	}
}

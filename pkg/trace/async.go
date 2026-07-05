package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// OverflowPolicy selects how the local async writer behaves when its queue is full.
type OverflowPolicy string

const (
	// OverflowDrop identifies the overflow drop constant used by this package.
	OverflowDrop   OverflowPolicy = "drop"
	OverflowBlock  OverflowPolicy = "block"
	OverflowSample OverflowPolicy = "sample"
)

// AsyncConfig sizes the JSONL writer queue and flush cadence.
type AsyncConfig struct {
	QueueSize      int
	FlushRecords   int
	FlushInterval  time.Duration
	OverflowPolicy OverflowPolicy
	SampleEvery    int
}

// DefaultAsyncConfig returns bounded queue settings that favor dropping over blocking RPCs.
func DefaultAsyncConfig() AsyncConfig {
	return AsyncConfig{
		QueueSize:      4096,
		FlushRecords:   64,
		FlushInterval:  100 * time.Millisecond,
		OverflowPolicy: OverflowDrop,
		SampleEvery:    10,
	}
}

var (
	ErrWriterClosed = errors.New("trace writer closed")
	bufferPool      = sync.Pool{
		New: func() any {
			return new(bytes.Buffer)
		},
	}
)

// AsyncJSONLWriter serializes trace records on one writer goroutine.
type AsyncJSONLWriter struct {
	queue         chan Record
	overflow      OverflowPolicy
	sampleEvery   int
	metrics       *PrometheusMetrics
	sampleCounter atomic.Uint64

	closeOnce sync.Once
	closed    atomic.Bool
	stopCh    chan struct{}
	doneCh    chan struct{}
	closeErr  error
}

// NewAsyncJSONLWriter opens a JSONL file and starts the background flush loop.
func NewAsyncJSONLWriter(path string, cfg AsyncConfig, metrics *PrometheusMetrics) (*AsyncJSONLWriter, error) {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultAsyncConfig().QueueSize
	}
	if cfg.FlushRecords <= 0 {
		cfg.FlushRecords = DefaultAsyncConfig().FlushRecords
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultAsyncConfig().FlushInterval
	}
	if cfg.OverflowPolicy == "" {
		cfg.OverflowPolicy = OverflowDrop
	}
	if cfg.SampleEvery <= 0 {
		cfg.SampleEvery = DefaultAsyncConfig().SampleEvery
	}
	if metrics == nil {
		metrics = DefaultPrometheusMetrics()
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	writer := &AsyncJSONLWriter{
		queue:       make(chan Record, cfg.QueueSize),
		overflow:    cfg.OverflowPolicy,
		sampleEvery: cfg.SampleEvery,
		metrics:     metrics,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
	go writer.run(file, cfg)
	return writer, nil
}

// NewDefaultAsyncJSONLWriter initializes default async jsonl writer with package defaults for this package's call path.
func NewDefaultAsyncJSONLWriter(path string) (*AsyncJSONLWriter, error) {
	return NewAsyncJSONLWriter(path, DefaultAsyncConfig(), DefaultPrometheusMetrics())
}

// Write enqueues one trace record according to the configured overflow policy.
func (w *AsyncJSONLWriter) Write(record Record) error {
	if w == nil {
		return nil
	}
	if w.closed.Load() {
		return ErrWriterClosed
	}
	if record.TimestampUnixMilli == 0 {
		record.TimestampUnixMilli = time.Now().UnixMilli()
	}

	switch w.overflow {
	case OverflowBlock:
		// Blocking is opt-in because it can put trace I/O on the RPC latency path.
		w.queue <- record
		return nil
	case OverflowSample:
		// Sampling sheds most overflow records but still admits periodic queue-full samples.
		select {
		case w.queue <- record:
			return nil
		default:
			if w.sampleCounter.Add(1)%uint64(w.sampleEvery) != 0 {
				w.metrics.IncDropped("queue_full_sampled")
				return nil
			}
			select {
			case w.queue <- record:
				return nil
			default:
				w.metrics.IncDropped("queue_full")
				return nil
			}
		}
	default:
		select {
		case w.queue <- record:
			return nil
		default:
			w.metrics.IncDropped("queue_full")
			return nil
		}
	}
}

// Close drains queued records, flushes the file, and is safe to call repeatedly.
func (w *AsyncJSONLWriter) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.stopCh)
		<-w.doneCh
	})
	return w.closeErr
}

// run owns file writes and buffer-pool reuse until Close drains the queue.
func (w *AsyncJSONLWriter) run(file *os.File, cfg AsyncConfig) {
	defer close(w.doneCh)

	bw := bufio.NewWriter(file)
	flushTicker := time.NewTicker(cfg.FlushInterval)
	defer flushTicker.Stop()

	pending := 0
	flush := func() {
		if pending == 0 {
			return
		}
		if err := bw.Flush(); err != nil && w.closeErr == nil {
			w.closeErr = err
		}
		pending = 0
	}

	writeRecord := func(record Record) {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := json.NewEncoder(buf).Encode(record); err != nil {
			bufferPool.Put(buf)
			if w.closeErr == nil {
				w.closeErr = err
			}
			return
		}
		// Copy encoded bytes into bufio before returning the buffer to the pool.
		// The writer goroutine owns the pool; callers must not retain buf.Bytes().
		if _, err := bw.Write(buf.Bytes()); err != nil && w.closeErr == nil {
			w.closeErr = err
		}
		bufferPool.Put(buf)
		pending++
		if pending >= cfg.FlushRecords {
			flush()
		}
	}

	for {
		select {
		case record := <-w.queue:
			writeRecord(record)
		case <-flushTicker.C:
			flush()
		case <-w.stopCh:
			flushTicker.Stop()
			for {
				select {
				case record := <-w.queue:
					writeRecord(record)
				default:
					flush()
					if err := file.Close(); err != nil && w.closeErr == nil {
						w.closeErr = err
					}
					return
				}
			}
		}
	}
}

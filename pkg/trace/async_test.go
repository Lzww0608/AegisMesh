package trace

import (
	"os"
	"testing"
	"time"

	"github.com/aegismesh/aegismesh/pkg/verifier"
	"github.com/prometheus/client_golang/prometheus"
)

// TestAsyncJSONLWriterWritesVerifierCompatibleRecord locks the async jsonl writer writes verifier compatible record contract so future changes do not regress it.
func TestAsyncJSONLWriterWritesVerifierCompatibleRecord(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	writer, err := NewAsyncJSONLWriter(path, DefaultAsyncConfig(), nil)
	if err != nil {
		t.Fatalf("new async trace writer: %v", err)
	}

	route := "user-service@10.0.0.2:7001"
	if err := writer.Write(Record{
		TraceID:       "trace-1",
		SpanID:        "span-1",
		Source:        "frontend",
		Destination:   "user-service",
		Method:        "/demo.shop.v1.UserService/GetUser",
		Route:         route,
		Path:          []string{"frontend", route},
		Upstream:      "10.0.0.2:7001",
		Attempt:       2,
		RetryAttempts: 1,
		Status:        "OK",
	}); err != nil {
		t.Fatalf("write trace record: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trace writer: %v", err)
	}

	traces := mustLoadTraces(t, path)
	if len(traces) != 1 {
		t.Fatalf("expected one trace, got %d", len(traces))
	}
	if traces[0].TraceID != "trace-1" || traces[0].Route != route || traces[0].RetryAttempts != 1 || traces[0].Status != "OK" {
		t.Fatalf("unexpected verifier trace: %+v", traces[0])
	}
}

// TestAsyncJSONLWriterDrainsOnClose locks the async jsonl writer drains on close contract so future changes do not regress it.
func TestAsyncJSONLWriterDrainsOnClose(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	cfg := DefaultAsyncConfig()
	cfg.FlushRecords = 1024
	cfg.FlushInterval = time.Hour

	writer, err := NewAsyncJSONLWriter(path, cfg, nil)
	if err != nil {
		t.Fatalf("new async trace writer: %v", err)
	}

	const records = 32
	for i := 0; i < records; i++ {
		if err := writer.Write(Record{
			TraceID:       "trace-batch",
			Route:         "user-service@10.0.0.2:7001",
			Path:          []string{"frontend", "user-service@10.0.0.2:7001"},
			RetryAttempts: 0,
			Status:        "OK",
		}); err != nil {
			t.Fatalf("write trace record %d: %v", i, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trace writer: %v", err)
	}

	traces := mustLoadTraces(t, path)
	if len(traces) != records {
		t.Fatalf("expected %d traces after drain, got %d", records, len(traces))
	}
}

// TestAsyncJSONLWriterBufferReturnedAfterWrite locks the async jsonl writer buffer returned after write contract so future changes do not regress it.
func TestAsyncJSONLWriterBufferReturnedAfterWrite(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	cfg := DefaultAsyncConfig()
	cfg.FlushRecords = 1
	writer, err := NewAsyncJSONLWriter(path, cfg, nil)
	if err != nil {
		t.Fatalf("new async trace writer: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := writer.Write(Record{TraceID: "trace", Route: "r", Path: []string{"a"}, Status: "OK"}); err != nil {
			t.Fatalf("write trace: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trace writer: %v", err)
	}
	traces := mustLoadTraces(t, path)
	if len(traces) != 8 {
		t.Fatalf("expected 8 traces, got %d", len(traces))
	}
}

// TestAsyncJSONLWriterDropsWhenQueueFull locks the async jsonl writer drops when queue full contract so future changes do not regress it.
func TestAsyncJSONLWriterDropsWhenQueueFull(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	reg := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(reg)
	if err != nil {
		t.Fatalf("new trace metrics: %v", err)
	}

	cfg := AsyncConfig{
		QueueSize:      1,
		FlushRecords:   1024,
		FlushInterval:  time.Hour,
		OverflowPolicy: OverflowDrop,
	}
	writer, err := NewAsyncJSONLWriter(path, cfg, metrics)
	if err != nil {
		t.Fatalf("new async trace writer: %v", err)
	}

	if err := writer.Write(Record{TraceID: "trace-1", Route: "r", Path: []string{"a"}, Status: "OK"}); err != nil {
		t.Fatalf("write first trace: %v", err)
	}
	if err := writer.Write(Record{TraceID: "trace-2", Route: "r", Path: []string{"a"}, Status: "OK"}); err != nil {
		t.Fatalf("write second trace: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trace writer: %v", err)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	found := false
	for _, family := range families {
		if family.GetName() != "aegis_dropped_traces_total" {
			continue
		}
		found = true
		if family.GetMetric()[0].GetCounter().GetValue() < 1 {
			t.Fatalf("expected dropped trace counter, got %+v", family)
		}
	}
	if !found {
		t.Fatalf("expected aegis_dropped_traces_total metric")
	}
}

// mustLoadTraces returns the requested value and fails the test immediately when setup is invalid.
func mustLoadTraces(t *testing.T, path string) []verifier.TraceRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace output: %v", err)
	}
	defer file.Close()

	traces, err := verifier.LoadTraceJSONL(file)
	if err != nil {
		t.Fatalf("load trace jsonl: %v", err)
	}
	return traces
}

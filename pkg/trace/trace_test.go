package trace

import (
	"os"
	"testing"

	"github.com/aegismesh/aegismesh/pkg/verifier"
)

func TestJSONLWriterWritesVerifierCompatibleRecord(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	writer, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatalf("new trace writer: %v", err)
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

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace output: %v", err)
	}
	defer file.Close()

	traces, err := verifier.LoadTraceJSONL(file)
	if err != nil {
		t.Fatalf("load trace jsonl: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected one trace, got %d", len(traces))
	}
	if traces[0].TraceID != "trace-1" || traces[0].Route != route || traces[0].RetryAttempts != 1 || traces[0].Status != "OK" {
		t.Fatalf("unexpected verifier trace: %+v", traces[0])
	}
}

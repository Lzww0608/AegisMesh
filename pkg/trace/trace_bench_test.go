package trace

import (
	"testing"
	"time"
)

func BenchmarkJSONLWriterWrite(b *testing.B) {
	path := b.TempDir() + "/sync.jsonl"
	writer, err := NewJSONLWriter(path)
	if err != nil {
		b.Fatalf("new sync trace writer: %v", err)
	}
	defer writer.Close()

	record := Record{
		TraceID:       "trace-bench",
		SpanID:        "span-bench",
		Source:        "frontend",
		Destination:   "user-service",
		Method:        "/demo.shop.v1.UserService/GetUser",
		Route:         "user-service@127.0.0.1:7001",
		Path:          []string{"frontend", "user-service@127.0.0.1:7001"},
		Upstream:      "127.0.0.1:7001",
		Attempt:       1,
		RetryAttempts: 0,
		Status:        "OK",
		TimestampUnixMilli: time.Now().UnixMilli(),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writer.Write(record); err != nil {
			b.Fatalf("write trace: %v", err)
		}
	}
}

func BenchmarkAsyncJSONLWriterWrite(b *testing.B) {
	path := b.TempDir() + "/async.jsonl"
	cfg := DefaultAsyncConfig()
	cfg.QueueSize = 1 << 16
	writer, err := NewAsyncJSONLWriter(path, cfg, nil)
	if err != nil {
		b.Fatalf("new async trace writer: %v", err)
	}
	defer writer.Close()

	record := Record{
		TraceID:       "trace-bench",
		SpanID:        "span-bench",
		Source:        "frontend",
		Destination:   "user-service",
		Method:        "/demo.shop.v1.UserService/GetUser",
		Route:         "user-service@127.0.0.1:7001",
		Path:          []string{"frontend", "user-service@127.0.0.1:7001"},
		Upstream:      "127.0.0.1:7001",
		Attempt:       1,
		RetryAttempts: 0,
		Status:        "OK",
		TimestampUnixMilli: time.Now().UnixMilli(),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writer.Write(record); err != nil {
			b.Fatalf("write trace: %v", err)
		}
	}
}

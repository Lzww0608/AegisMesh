package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Writer interface {
	Write(record Record) error
	Close() error
}

type Record struct {
	TraceID            string   `json:"trace_id"`
	SpanID             string   `json:"span_id,omitempty"`
	ParentSpanID       string   `json:"parent_span_id,omitempty"`
	TimestampUnixMilli int64    `json:"timestamp_unix_ms,omitempty"`
	Source             string   `json:"source,omitempty"`
	Destination        string   `json:"destination,omitempty"`
	Method             string   `json:"method,omitempty"`
	Route              string   `json:"route"`
	Path               []string `json:"path"`
	Upstream           string   `json:"upstream,omitempty"`
	Attempt            int      `json:"attempt,omitempty"`
	RetryAttempts      int      `json:"retry_attempts"`
	Status             string   `json:"status"`
}

type JSONLWriter struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func NewJSONLWriter(path string) (*JSONLWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &JSONLWriter{
		file: file,
		enc:  json.NewEncoder(file),
	}, nil
}

func (w *JSONLWriter) Write(record Record) error {
	if w == nil {
		return nil
	}
	if record.TimestampUnixMilli == 0 {
		record.TimestampUnixMilli = time.Now().UnixMilli()
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(record)
}

func (w *JSONLWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.file.Close()
	w.file = nil
	return err
}

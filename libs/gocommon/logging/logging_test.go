package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONOutputCarriesServiceAndTraceID(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithWriter(&buf, "indexer", "INFO")

	ctx := WithTraceID(context.Background(), "job-42")
	log.InfoContext(ctx, "chunked file", "path", "main.go", "chunks", 3)
	log.DebugContext(ctx, "hidden at INFO")
	log.Warn("no trace")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first["service"] != "indexer" || first["trace_id"] != "job-42" || first["msg"] != "chunked file" {
		t.Errorf("unexpected first line: %v", first)
	}
	if first["chunks"] != float64(3) {
		t.Errorf("extra attrs missing: %v", first)
	}
	if _, ok := second["trace_id"]; ok {
		t.Errorf("trace_id leaked into untraced line: %v", second)
	}
	if second["level"] != "WARNING" {
		t.Errorf("level = %v, want WARNING", second["level"])
	}
}

func TestTraceIDSurvivesWith(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithWriter(&buf, "svc", "DEBUG").With("component", "worker")
	log.InfoContext(WithTraceID(context.Background(), "t1"), "hello")
	if !strings.Contains(buf.String(), `"trace_id":"t1"`) || !strings.Contains(buf.String(), `"component":"worker"`) {
		t.Fatalf("got %s", buf.String())
	}
}

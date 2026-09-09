package filetrace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// readRecords 读取日志文件中的全部 JSON Lines 记录。
func readRecords(t *testing.T, path string) []*logRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace log: %v", err)
	}
	defer f.Close()

	var records []*logRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal trace line %q: %v", line, err)
		}
		records = append(records, &rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace log: %v", err)
	}
	return records
}

func TestProviderWritesJSONLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tracing.log")
	tp, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tp.Shutdown(context.Background())

	tr := tp.Tracer("test")
	ctx, span := tr.Start(context.Background(), "root",
		trace.WithAttributes(attribute.String("k", "v")))
	span.SetStatus(codes.Ok, "all good")
	span.End()

	// 子 span
	_, child := tr.Start(ctx, "child")
	child.RecordError(errors.New("boom"))
	child.End()

	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	root := records[0]
	if root.Name != "root" {
		t.Errorf("root name = %q, want root", root.Name)
	}
	if root.ParentSpanID != "" {
		t.Errorf("root should not have parent_span_id")
	}
	if root.Attributes["k"] != "v" {
		t.Errorf("root attribute k = %v", root.Attributes["k"])
	}
	if root.StatusCode != codes.Ok.String() {
		t.Errorf("root status = %q", root.StatusCode)
	}

	childRec := records[1]
	if childRec.Name != "child" {
		t.Errorf("child name = %q, want child", childRec.Name)
	}
	if childRec.ParentSpanID != root.SpanID {
		t.Errorf("child parent_span_id = %q, want %q", childRec.ParentSpanID, root.SpanID)
	}
	if childRec.TraceID != root.TraceID {
		t.Error("child trace_id should equal root trace_id")
	}
	if len(childRec.Events) != 1 || childRec.Events[0].Name != "exception" {
		t.Errorf("child events = %+v, want one exception", childRec.Events)
	}
}

func TestConcurrentSpans(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tracing.log")
	tp, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tp.Shutdown(context.Background())

	tr := tp.Tracer("test")
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			_, span := tr.Start(context.Background(), "parallel",
				trace.WithAttributes(attribute.Int("idx", idx)))
			span.End()
		}(i)
	}
	wg.Wait()

	records := readRecords(t, path)
	if len(records) != n {
		t.Fatalf("expected %d records, got %d", n, len(records))
	}
	ids := make(map[string]bool)
	for _, r := range records {
		if ids[r.SpanID] {
			t.Fatalf("duplicate span_id %q", r.SpanID)
		}
		ids[r.SpanID] = true
	}
}

func TestShutdownClosesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tracing.log")
	tp, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	tr := tp.Tracer("test")
	_, span := tr.Start(context.Background(), "after-shutdown")
	// End 时写文件会失败，但不会 panic；这里只验证不崩溃。
	span.End()
}

package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

// trackingRegistry 包装 DefaultRegistry，统计并发执行期间同时在跑的调用数，
// 验证 read_file 批量执行确实并行（fork-join）而非顺序。
type trackingRegistry struct {
	tools.Registry
	inFlight  atomic.Int64
	maxFlight atomic.Int64
}

func (t *trackingRegistry) Execute(ctx context.Context, toolCall *schema.ToolCall) *schema.ToolResult {
	cur := t.inFlight.Add(1)
	defer t.inFlight.Add(-1)
	for {
		m := t.maxFlight.Load()
		if cur <= m || t.maxFlight.CompareAndSwap(m, cur) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond) // 放大窗口，确保并发可被观测
	return t.Registry.Execute(ctx, toolCall)
}

func TestExecuteToolCalls_ParallelReadFile(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"a.txt": "content a\nline2\n",
		"b.txt": "content b\n",
		"c.txt": "content c\n",
	}
	workDir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(workDir+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := &trackingRegistry{Registry: tools.NewDefaultRegistry(nil, nil)}
	reg.Register(tools.NewReadFileTool(workDir))
	eng := NewAgentEngine(reg, nil, workDir, false, newSession(t.TempDir(), "test-session"), nil)

	calls := make([]schema.ToolCall, 0, len(files))
	for name := range files {
		calls = append(calls, schema.ToolCall{
			ID:   "call-" + name,
			Name: tools.ToolReadFile,
			Arguments: mustJSON(t, map[string]any{
				"path":          name,
				"start_line_no": 1,
				"start_bytes":   1,
			}),
		})
	}

	results := eng.executeToolCalls(context.Background(), calls)

	// join 结果必须与入参顺序一一对应（tool_call_id 与内容匹配）
	for i, call := range calls {
		res := results[i]
		if res.Error != nil {
			t.Fatalf("call %s: unexpected error: %v", call.ID, res.Error)
		}
		if res.ToolCallID != call.ID {
			t.Fatalf("result %d: got ToolCallID %q, want %q", i, res.ToolCallID, call.ID)
		}
		want := "content " + strings.TrimSuffix(strings.TrimPrefix(call.ID, "call-"), ".txt")
		if !strings.Contains(res.Output, want) {
			t.Fatalf("result %d: output %q does not contain %q", i, res.Output, want)
		}
	}

	// 并行性校验：3 个调用应同时观测到 >1 的 in-flight
	if reg.maxFlight.Load() < 2 {
		t.Fatalf("expected parallel execution (max in-flight >= 2), got %d", reg.maxFlight.Load())
	}
}

func TestExecuteToolCalls_SequentialForMixedCalls(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	if err := os.WriteFile(workDir+"/a.txt", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := &trackingRegistry{Registry: tools.NewDefaultRegistry(nil, nil)}
	reg.Register(tools.NewReadFileTool(workDir))
	eng := NewAgentEngine(reg, nil, workDir, false, newSession(t.TempDir(), "test-session"), nil)

	// 混入非 read_file 调用，应退化为顺序执行
	calls := []schema.ToolCall{
		{ID: "call-1", Name: tools.ToolReadFile, Arguments: mustJSON(t, map[string]any{"path": "a.txt", "start_line_no": 1, "start_bytes": 1})},
		{ID: "call-2", Name: "unknown_tool", Arguments: mustJSON(t, map[string]any{})},
	}

	results := eng.executeToolCalls(context.Background(), calls)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[1].Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if reg.maxFlight.Load() > 1 {
		t.Fatalf("mixed batch should run sequentially, max in-flight = %d", reg.maxFlight.Load())
	}
}

func TestBuildToolResultContent(t *testing.T) {
	t.Parallel()

	content := buildToolResultContent(tools.ToolReadFile, &schema.ToolResult{
		Error:      errTest,
		Output:     "partial output",
		ToolCallID: "call-1",
	})
	for _, want := range []string{"error executing tool read_file", "partial output"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content %q missing %q", content, want)
		}
	}

	ok := buildToolResultContent(tools.ToolReadFile, &schema.ToolResult{Output: "ok", ToolCallID: "call-2"})
	if ok != "ok" {
		t.Fatalf("success path: got %q, want %q", ok, "ok")
	}
}

var errTest = errTestType{}

type errTestType struct{}

func (errTestType) Error() string { return "boom" }

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

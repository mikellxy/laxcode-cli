package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"go.opentelemetry.io/otel/trace"
)

// stubTool 是仅用于注册表测试的 BaseTool：Execute 行为由回调决定。
type stubTool struct {
	name       string
	execFn     func(ctx context.Context, args json.RawMessage) (string, error)
	beforeInfo string
	afterInfo  string
	closeErr   error
	closed     bool
}

func (s *stubTool) Name() string { return s.name }

func (s *stubTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{Name: s.name, Description: "stub " + s.name}
}

func (s *stubTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if s.execFn != nil {
		return s.execFn(ctx, args)
	}
	return "ok", nil
}

func (s *stubTool) BeforeExecInfo(json.RawMessage) string {
	if s.beforeInfo != "" {
		return s.beforeInfo
	}
	return s.name + "()"
}

func (s *stubTool) AfterExecInfo(json.RawMessage) string { return s.afterInfo }

func (s *stubTool) Close() error {
	s.closed = true
	return s.closeErr
}

func TestRegistryRegisterAndGetAvailableTools(t *testing.T) {
	r := NewDefaultRegistry(nil)
	r.Register(&stubTool{name: "bash"})
	r.Register(&stubTool{name: "read_file"})

	defs := r.GetAvailableTools()
	if len(defs) != 2 {
		t.Fatalf("应返回 2 个工具定义，实际 %d", len(defs))
	}
	names := []string{defs[0].Name, defs[1].Name}
	sort.Strings(names)
	if names[0] != "bash" || names[1] != "read_file" {
		t.Errorf("工具集合不符：%v", names)
	}
}

func TestRegistryExecuteSuccess(t *testing.T) {
	r := NewDefaultRegistry(nil)
	r.Register(&stubTool{name: "echo_tool", execFn: func(_ context.Context, args json.RawMessage) (string, error) {
		var m map[string]string
		_ = json.Unmarshal(args, &m)
		return "hi " + m["who"], nil
	}})

	call := &sharedkernel.ToolCall{
		ID:        "call-1",
		Name:      "echo_tool",
		Arguments: json.RawMessage(`{"who":"world"}`),
	}
	res := r.Execute(context.Background(), call)
	if res.Error != nil {
		t.Fatalf("不应有错误：%v", res.Error)
	}
	if res.IsError {
		t.Error("成功执行不应标记 IsError")
	}
	if res.Output != "hi world" {
		t.Errorf("输出不符：%q", res.Output)
	}
	if res.ToolCallID != "call-1" {
		t.Errorf("ToolCallID 透传不符：%q", res.ToolCallID)
	}
}

func TestRegistryExecuteUnknownTool(t *testing.T) {
	r := NewDefaultRegistry(nil)
	res := r.Execute(context.Background(), &sharedkernel.ToolCall{
		ID:   "c-1",
		Name: "ghost",
	})
	if res.Error == nil {
		t.Fatal("未知工具应返回错误")
	}
	if !res.IsError {
		t.Error("未知工具应标记 IsError")
	}
	if !strings.Contains(res.Output, "tool ghost not exists") {
		t.Errorf("输出应说明工具不存在：%q", res.Output)
	}
}

func TestRegistryExecuteErrorCarriesPrompt(t *testing.T) {
	r := NewDefaultRegistry(nil)
	r.Register(&stubTool{name: "fail_tool", execFn: func(context.Context, json.RawMessage) (string, error) {
		return "partial out", NewErrorWithPrompt(&ParamError{}, errors.New("bad arg"))
	}})

	res := r.Execute(context.Background(), &sharedkernel.ToolCall{Name: "fail_tool"})
	if res.Error == nil {
		t.Fatal("应返回错误")
	}
	if !res.IsError {
		t.Error("出错应标记 IsError")
	}
	if !strings.Contains(res.Output, "error executing tool fail_tool") {
		t.Errorf("输出应以错误开头：%q", res.Output)
	}
	if !strings.Contains(res.Output, "### suggestion:") {
		t.Errorf("输出应包含自愈引导提示：%q", res.Output)
	}
	if !strings.Contains(res.Output, "partial out") {
		t.Errorf("输出应附原始输出供定位：%q", res.Output)
	}
}

func TestRegistryBeforeExecInfo(t *testing.T) {
	r := NewDefaultRegistry(nil)
	r.Register(&stubTool{name: "bash", beforeInfo: "bash(ls -la)"})

	if got := r.BeforeExecInfo(&sharedkernel.ToolCall{Name: "bash"}); got != "bash(ls -la)" {
		t.Errorf("应返回工具 before info，实际 %q", got)
	}
	if got := r.BeforeExecInfo(&sharedkernel.ToolCall{Name: "not-registered"}); got != "" {
		t.Errorf("未注册工具应返回空串，实际 %q", got)
	}
}

func TestRegistryCloseClosesClosers(t *testing.T) {
	r := NewDefaultRegistry(nil)
	withCloser := &stubTool{name: "a"}
	withoutCloser := &stubTool{name: "b"}
	// 去掉 withCloser 的 Close？—— stubTool 恒实现 Closer，
	// 故这里直接验证实现了 Closer 的工具被调用即可
	r.Register(withCloser)
	r.Register(withoutCloser)

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !withCloser.closed || !withoutCloser.closed {
		t.Error("所有实现 Closer 的工具都应在 Close 时被回收")
	}
}

func TestRegistryCloseJoinsErrors(t *testing.T) {
	r := NewDefaultRegistry(nil)
	r.Register(&stubTool{name: "bad", closeErr: errors.New("close boom")})
	if err := r.Close(); err == nil {
		t.Fatal("Close 应汇总子工具错误")
	} else if !strings.Contains(err.Error(), "close boom") {
		t.Errorf("应包含子错误信息，实际 %v", err)
	}
}

func TestBuildToolResultContentNilError(t *testing.T) {
	if got := buildToolResultContent("bash", nil, "out"); got != "out" {
		t.Errorf("无错误应原样返回输出，实际 %q", got)
	}
}

func TestToolResultAsMsg(t *testing.T) {
	res := &sharedkernel.ToolResult{Output: "final", ToolCallID: "tc-9"}
	msg := ToolResultAsMsg(res)
	if msg.Role != sharedkernel.RoleTool {
		t.Errorf("角色应为 tool，实际 %q", msg.Role)
	}
	if msg.ToolCallID != "tc-9" || msg.Content != "final" {
		t.Errorf("应透传 ToolCallID 与 Output，实际 %+v", msg)
	}
}

func TestRegistryExecuteWithTracingAndSessionID(t *testing.T) {
	// 覆盖 Execute 内部埋点路径：ctx 携带 session_id 时不应 panic，
	// 注册表默认 noop tracer 零成本执行
	ctx := telemetry.ContextWithSessionID(context.Background(), "sess-trace")
	r := NewDefaultRegistry(trace.NewNoopTracerProvider().Tracer("x"))
	r.Register(&stubTool{name: "t"})
	res := r.Execute(ctx, &sharedkernel.ToolCall{Name: "t"})
	if res.Error != nil {
		t.Fatalf("执行失败：%v", res.Error)
	}
	if res.Output != "ok" {
		t.Errorf("输出不符：%q", res.Output)
	}
	// 顺带校验 fmt 未引入意外符号
	if !strings.HasPrefix(res.Output, "ok") {
		t.Errorf("异常输出 %q", res.Output)
	}
}

func TestToolNameConstants(t *testing.T) {
	want := map[string]string{
		ToolBash:        "bash",
		ToolReadFile:    "read_file",
		ToolWriteFile:   "write_file",
		ToolEditFile:    "edit_file",
		ToolRunSubAgent: "run_sub_agent",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("常量 %q 应等于 %q", got, w)
		}
	}
}

func TestExecResultString(t *testing.T) {
	res := ExecResult{ExitCode: 1, Stdout: "boom", Desc: "命令执行失败"}
	s := res.String()
	if !strings.Contains(s, "exit_code:1") || !strings.Contains(s, "stdout:boom") {
		t.Errorf("ExecResult.String 输出不符：%q", s)
	}
}

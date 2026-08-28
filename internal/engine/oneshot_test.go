package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

// mockProvider 按预设返回固定消息或错误，绕开真实模型调用。
type mockProvider struct {
	msg *schema.Message
	err error
}

func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	if m.err != nil {
		return nil, m.err
	}
	cp := *m.msg
	return &cp, nil
}

func (m *mockProvider) Info() *provider.Info { return &provider.Info{Name: "mock"} }

// newOneShotEngine 构造静默输出的测试引擎：registry 与 engine 的打印
// 均走 DiscardPrinter，避免测试输出被中间过程污染。
func newOneShotEngine(t *testing.T, sess *Session, p provider.Provider) *AgentEngine {
	t.Helper()
	eng := NewAgentEngine(tools.NewDefaultRegistry(printer.DiscardPrinter{}), p, t.TempDir(), false, sess)
	eng.Printer = printer.DiscardPrinter{}
	return eng
}

// captureStdout 捕获 fn 执行期间写到 os.Stdout 的内容（OneShotLoop 的
// 契约 JSON 直写 os.Stdout，不经 Printer）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("关闭管道失败: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取管道失败: %v", err)
	}
	return string(out)
}

// parseOneShotJSON 把捕获的 stdout 解析为 map，断言为单行合法 JSON。
func parseOneShotJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stdout 应恰好一行 JSON，实际 %d 行: %q", len(lines), out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("stdout 不是合法 JSON: %v\n%q", err, out)
	}
	return m
}

func TestOneShotLoopSuccess(t *testing.T) {
	sess := newSession(t.TempDir(), "s-success")
	sess.RecordGenerate(RoundStat{TimeUsed: 10, TokenInput: 100, TokenOutput: 20})
	sess.RecordGenerate(RoundStat{TimeUsed: 10, TokenInput: 300, TokenOutput: 40})

	p := &mockProvider{msg: &schema.Message{Role: schema.RoleAssistant, Content: "任务完成"}}
	eng := newOneShotEngine(t, sess, p)

	var runErr error
	out := captureStdout(t, func() {
		runErr = OneShotLoop(context.Background(), eng, "修复登录bug")
	})
	if runErr != nil {
		t.Fatalf("成功路径不应返回错误: %v", runErr)
	}

	m := parseOneShotJSON(t, out)
	if m["session_id"] != "s-success" {
		t.Errorf("session_id = %v, want s-success", m["session_id"])
	}
	if m["result"] != "任务完成" {
		t.Errorf("result = %v, want 任务完成", m["result"])
	}
	if m["error"] != nil {
		t.Errorf("error = %v, want nil", m["error"])
	}
	// token 统计来自 session：累计 400/60，窗口为最后一轮 300/40
	tu := m["token_used"].(map[string]any)
	if tu["token_input"].(float64) != 400 || tu["token_output"].(float64) != 60 {
		t.Errorf("token_used = %v, want 400/60", tu)
	}
	wt := m["window_token"].(map[string]any)
	if wt["token_input"].(float64) != 300 || wt["token_output"].(float64) != 40 {
		t.Errorf("window_token = %v, want 300/40", wt)
	}
	// rounds 不出现在返回中
	if _, ok := m["rounds"]; ok {
		t.Errorf("返回不应包含 rounds 字段: %v", m)
	}

	// user 消息应已追加进历史（单次执行语义）
	if len(sess.Messages) == 0 || sess.Messages[0].Content != "修复登录bug" {
		t.Errorf("task 未追加进 session 历史: %+v", sess.Messages)
	}
}

func TestOneShotLoopGenerateFailure(t *testing.T) {
	sess := newSession(t.TempDir(), "s-fail")
	sess.RecordGenerate(RoundStat{TimeUsed: 10, TokenInput: 100, TokenOutput: 20})

	p := &mockProvider{err: errors.New("api boom")}
	eng := newOneShotEngine(t, sess, p)

	var runErr error
	out := captureStdout(t, func() {
		runErr = OneShotLoop(context.Background(), eng, "任务")
	})
	if runErr == nil {
		t.Fatal("失败路径应返回非 nil 错误供 main 映射 exit code")
	}

	m := parseOneShotJSON(t, out)
	if m["result"] != "" {
		t.Errorf("失败时 result 应为空串，实际 %v", m["result"])
	}
	errObj := m["error"].(map[string]any)
	if errObj["type"] != ErrTypeGenerate {
		t.Errorf("error.type = %v, want %s", errObj["type"], ErrTypeGenerate)
	}
	if !strings.Contains(errObj["message"].(string), "api boom") {
		t.Errorf("error.message 应含原始错误信息: %v", errObj["message"])
	}
	// 失败仍返回 token 统计：钱已花，调用方有权知道
	tu := m["token_used"].(map[string]any)
	if tu["token_input"].(float64) != 100 || tu["token_output"].(float64) != 20 {
		t.Errorf("失败时 token_used 应保留，实际 %v", tu)
	}
	if m["session_id"] != "s-fail" {
		t.Errorf("失败时 session_id 应保留，实际 %v", m["session_id"])
	}
}

func TestOneShotLoopTooManyTurns(t *testing.T) {
	sess := newSession(t.TempDir(), "s-turns")
	// 每轮都返回带工具调用的消息（工具不存在，registry 返回错误结果），
	// 50 轮后 Run 触发 errTooManyTurns
	p := &mockProvider{msg: &schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "继续",
		ToolCalls: []schema.ToolCall{{ID: "c1", Name: "unknown_tool", Arguments: json.RawMessage(`{}`)}},
	}}
	eng := newOneShotEngine(t, sess, p)

	var runErr error
	out := captureStdout(t, func() {
		runErr = OneShotLoop(context.Background(), eng, "任务")
	})
	if !errors.Is(runErr, errTooManyTurns) {
		t.Fatalf("应返回 errTooManyTurns，实际 %v", runErr)
	}

	m := parseOneShotJSON(t, out)
	errObj := m["error"].(map[string]any)
	if errObj["type"] != ErrTypeTooManyTurns {
		t.Errorf("error.type = %v, want %s", errObj["type"], ErrTypeTooManyTurns)
	}
	if m["session_id"] != "s-turns" {
		t.Errorf("session_id = %v, want s-turns", m["session_id"])
	}
}

func TestOneShotErrTypeMapping(t *testing.T) {
	if got := oneShotErrType(errTooManyTurns); got != ErrTypeTooManyTurns {
		t.Errorf("errTooManyTurns 映射为 %s, want %s", got, ErrTypeTooManyTurns)
	}
	wrapped := fmt.Errorf("generating message: %w", errTooManyTurns)
	if got := oneShotErrType(wrapped); got != ErrTypeTooManyTurns {
		t.Errorf("包装后的 errTooManyTurns 应映射为 %s, 实际 %s", ErrTypeTooManyTurns, got)
	}
	if got := oneShotErrType(errors.New("other")); got != ErrTypeGenerate {
		t.Errorf("其余错误映射为 %s, want %s", got, ErrTypeGenerate)
	}
}

func TestNewUsageResult(t *testing.T) {
	res := NewUsageResult("-workdir is required")
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["session_id"] != "" {
		t.Errorf("用法错误 session_id 应为空串，实际 %v", m["session_id"])
	}
	if m["result"] != "" {
		t.Errorf("用法错误 result 应为空串，实际 %v", m["result"])
	}
	errObj := m["error"].(map[string]any)
	if errObj["type"] != ErrTypeUsage {
		t.Errorf("error.type = %v, want %s", errObj["type"], ErrTypeUsage)
	}
	tu := m["token_used"].(map[string]any)
	if tu["token_input"].(float64) != 0 || tu["token_output"].(float64) != 0 {
		t.Errorf("用法错误 token 统计应为零值，实际 %v", tu)
	}
}

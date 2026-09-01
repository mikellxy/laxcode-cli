package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// ---------- 内存 TracerProvider 测试桩 ----------
// 产品约束只依赖 OTel API 模块，故不引 SDK 的 tracetest，自行实现接口。
// 三个接口（TracerProvider/Tracer/Span）均含私有标记方法，按官方
// "API Implementations" 姿势嵌入 embedded.Xxx 满足。全部方法并发安全
// （并行 read_file 的 goroutine 会并发创建与结束 span）。

type memProvider struct {
	embedded.TracerProvider
	tracer *memTracer
}

func newMemProvider() *memProvider {
	return &memProvider{tracer: &memTracer{}}
}

func (p *memProvider) Tracer(string, ...trace.TracerOption) trace.Tracer { return p.tracer }

type memTracer struct {
	embedded.Tracer
	mu     sync.Mutex
	spans  []*memSpan
	nextID uint64
}

func (m *memTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	s := &memSpan{tracer: m, name: name, attrs: map[attribute.Key]attribute.Value{}}
	binary.BigEndian.PutUint64(s.spanID[:], m.nextID)
	// 父链：ctx 中已有本桩 span 则继承其 trace 并记录父子；否则开新 trace
	if p, ok := trace.SpanFromContext(ctx).(*memSpan); ok {
		s.parent = p
		s.traceID = p.traceID
	} else {
		binary.BigEndian.PutUint64(s.traceID[8:], m.nextID)
	}
	cfg := trace.NewSpanStartConfig(opts...)
	for _, kv := range cfg.Attributes() {
		s.attrs[kv.Key] = kv.Value
	}
	m.spans = append(m.spans, s)
	return trace.ContextWithSpan(ctx, s), s
}

// snapshot 拷出当前全部 span；调用前须确保被测流程已结束（无并发写）
func (m *memTracer) snapshot() []*memSpan {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*memSpan, len(m.spans))
	copy(out, m.spans)
	return out
}

type memSpan struct {
	embedded.Span
	tracer  *memTracer
	name    string
	attrs   map[attribute.Key]attribute.Value
	status  codes.Code
	errCnt  int
	parent  *memSpan
	traceID trace.TraceID
	spanID  trace.SpanID
	ended   bool
}

func (s *memSpan) End(...trace.SpanEndOption) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.ended = true
}
func (s *memSpan) AddEvent(string, ...trace.EventOption) {}
func (s *memSpan) AddLink(trace.Link)                    {}
func (s *memSpan) IsRecording() bool                     { return true }
func (s *memSpan) RecordError(error, ...trace.EventOption) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.errCnt++
}
func (s *memSpan) SpanContext() trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: s.traceID, SpanID: s.spanID})
}
func (s *memSpan) SetName(name string) { s.tracer.mu.Lock(); defer s.tracer.mu.Unlock(); s.name = name }
func (s *memSpan) SetStatus(c codes.Code, _ string) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.status = c
}
func (s *memSpan) SetAttributes(kvs ...attribute.KeyValue) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	for _, kv := range kvs {
		s.attrs[kv.Key] = kv.Value
	}
}
func (s *memSpan) TracerProvider() trace.TracerProvider { return nil }

// ---------- 断言辅助 ----------

func spansNamed(spans []*memSpan, name string) []*memSpan {
	var out []*memSpan
	for _, s := range spans {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func attrStr(s *memSpan, k attribute.Key) string {
	if v, ok := s.attrs[k]; ok {
		return v.AsString()
	}
	return ""
}

func attrInt(s *memSpan, k attribute.Key) int64 {
	if v, ok := s.attrs[k]; ok {
		return v.AsInt64()
	}
	return 0
}

// requireSpan 按名取唯一 span，数量不符即失败
func requireSpan(t *testing.T, spans []*memSpan, name string, want int) []*memSpan {
	t.Helper()
	got := spansNamed(spans, name)
	if len(got) != want {
		t.Fatalf("span %q 数量 = %d, want %d", name, len(got), want)
	}
	return got
}

// ---------- scripted provider：按队列依次返回消息 ----------

type scriptedProvider struct {
	queue []*schema.Message
	calls int
}

func (p *scriptedProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	if p.calls >= len(p.queue) {
		return nil, errors.New("script exhausted")
	}
	msg := p.queue[p.calls]
	p.calls++
	cp := *msg
	return &cp, nil
}

func (p *scriptedProvider) Info() *provider.Info { return &provider.Info{Name: "scripted"} }

// newTracedEngine 装配带内存追踪与静默输出的测试引擎
func newTracedEngine(t *testing.T, mp *memProvider, reg *tools.DefaultRegistry, p provider.Provider, sess *Session) *AgentEngine {
	t.Helper()
	eng := NewAgentEngine(reg, p, t.TempDir(), false, sess, mp.tracer)
	eng.Printer = printer.DiscardPrinter{}
	return eng
}

// TestRunSpanTree 验证一次两循环运行的完整 span 树（one-shot 形态：
// agent-run 为 root），含 token 属性口径与并行 read_file 的兄弟 span。
func TestRunSpanTree(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(workDir+"/"+f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mp := newMemProvider()
	reg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, mp.tracer)
	reg.Register(tools.NewReadFileTool(workDir))

	sp := &scriptedProvider{queue: []*schema.Message{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: tools.ToolReadFile, Arguments: mustJSON(t, map[string]any{"path": "a.txt", "start_line_no": 1, "start_bytes": 1})},
			{ID: "c2", Name: tools.ToolReadFile, Arguments: mustJSON(t, map[string]any{"path": "b.txt", "start_line_no": 1, "start_bytes": 1})},
		}, TokenUsed: schema.TokenStatistics{TokenInput: 100, TokenOutput: 20}},
		{Role: schema.RoleAssistant, Content: "done", TokenUsed: schema.TokenStatistics{TokenInput: 50, TokenOutput: 10}},
	}}
	sess := newSession(t.TempDir(), "s-tree")
	eng := newTracedEngine(t, mp, reg, sp, sess)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 应成功: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}

	spans := mp.tracer.snapshot()
	run := requireSpan(t, spans, tracing.SpanReAct, 1)[0]
	loops := requireSpan(t, spans, tracing.LLMTurn, 2)
	gens := requireSpan(t, spans, tracing.SpanLLMGenerate, 2)
	toolSpans := requireSpan(t, spans, tracing.SpanToolExec, 2)

	// agent-run：root（one-shot 形态）、角色与 session、token 合计
	if run.parent != nil {
		t.Error("one-shot 形态下 agent-run 应为 root（无父 span）")
	}
	if got := attrStr(run, tracing.AttrAgentRole); got != tracing.AgentRoleMain {
		t.Errorf("agent_role = %q, want main", got)
	}
	if got := attrStr(run, tracing.AttrSessionID); got != "s-tree" {
		t.Errorf("session_id = %q, want s-tree", got)
	}
	if in, out := attrInt(run, tracing.AttrInputTokens), attrInt(run, tracing.AttrOutputTokens); in != 150 || out != 30 {
		t.Errorf("run token 合计 = %d/%d, want 150/30", in, out)
	}

	// react-loop：父为 run、序号 1/2、本轮 token
	for i, loop := range loops {
		if loop.parent != run {
			t.Errorf("loop %d 的父应为 agent-run", i)
		}
		if got := attrInt(loop, tracing.AttrTurnSeq); got != int64(i+1) {
			t.Errorf("loop %d 的 loop_seq = %d", i, got)
		}
	}
	if in := attrInt(loops[0], tracing.AttrInputTokens); in != 100 {
		t.Errorf("loop1 token_input = %d, want 100", in)
	}

	// llm-generate：父为对应 loop、token 与工具调用数
	for i, gen := range gens {
		if gen.parent != loops[i] {
			t.Errorf("gen %d 的父应为对应 react-loop", i)
		}
	}
	if got := attrInt(gens[0], tracing.AttrToolCallCount); got != 2 {
		t.Errorf("gen1 tool_call_count = %d, want 2", got)
	}
	if in := attrInt(gens[1], tracing.AttrInputTokens); in != 50 {
		t.Errorf("gen2 token_input = %d, want 50", in)
	}

	// tool-exec：并行兄弟——父同为 loop1、不同 spanID、同 trace、带 tool_name
	for _, ts := range toolSpans {
		if ts.parent != loops[0] {
			t.Error("tool-exec 的父应为 loop1（并行分支沿用同一 ctx）")
		}
		if got := attrStr(ts, tracing.AttrToolName); got != tools.ToolReadFile {
			t.Errorf("tool_name = %q, want read_file", got)
		}
		if got := attrStr(ts, tracing.AttrSessionID); got != "s-tree" {
			t.Errorf("tool-exec session_id = %q, want s-tree", got)
		}
		if _, ok := ts.attrs[tracing.AttrInputTokens]; ok {
			t.Error("tool-exec 不应携带 token 属性")
		}
	}
	if toolSpans[0].spanID == toolSpans[1].spanID {
		t.Error("并行兄弟 span 的 spanID 应不同")
	}

	// 全树同一条 trace、全部已结束
	for _, s := range spans {
		if s.traceID != run.traceID {
			t.Errorf("span %q 与 root 不在同一 trace", s.name)
		}
		if !s.ended {
			t.Errorf("span %q 未结束", s.name)
		}
	}
}

// TestTerminalTaskRootSpan 验证交互模式：每次用户输入开启一条新 trace，
// terminal-task 为 root，token 合计取 Run 前后 session 累计差值。
func TestTerminalTaskRootSpan(t *testing.T) {
	t.Parallel()

	mp := newMemProvider()
	reg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, mp.tracer)
	sp := &scriptedProvider{queue: []*schema.Message{
		{Role: schema.RoleAssistant, Content: "答", TokenUsed: schema.TokenStatistics{TokenInput: 30, TokenOutput: 7}},
	}}
	sess := newSession(t.TempDir(), "s-term")
	// terminal-task 的 token 合计依赖 session 记账，provider 须如线上
	// 装配（assembleEngine）一样经 MonitoredProvider 包装
	eng := newTracedEngine(t, mp, reg, NewMonitoredProvider(sp, sess), sess)

	// 以管道替换 stdin：一轮输入 "你好" 后 "exit" 退出
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(w, "你好\nexit\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if err := TerminalLoop(context.Background(), eng); err != nil {
		t.Fatalf("TerminalLoop 应正常退出: %v", err)
	}

	spans := mp.tracer.snapshot()
	task := requireSpan(t, spans, tracing.SpanTerminalTask, 1)[0]
	run := requireSpan(t, spans, tracing.SpanReAct, 1)[0]

	if task.parent != nil {
		t.Error("terminal-task 应为 trace root")
	}
	if got := attrInt(task, tracing.AttrTaskSeq); got != 1 {
		t.Errorf("task_seq = %d, want 1", got)
	}
	if got := attrStr(task, tracing.AttrSessionID); got != "s-term" {
		t.Errorf("session_id = %q, want s-term", got)
	}
	if in, out := attrInt(task, tracing.AttrInputTokens), attrInt(task, tracing.AttrOutputTokens); in != 30 || out != 7 {
		t.Errorf("terminal-task token 合计 = %d/%d, want 30/7", in, out)
	}
	if run.parent != task {
		t.Error("agent-run 应挂在 terminal-task 下")
	}
	if run.traceID != task.traceID {
		t.Error("agent-run 与 terminal-task 应在同一 trace")
	}
}

// spawnSubTool 复刻 SubAgent.Execute 的嵌套机制（建子引擎并 Run），
// 但以 scripted provider 替换真实 LLM 调用，供追踪嵌套验证。
type spawnSubTool struct {
	tracer trace.Tracer
	t      *testing.T
}

func (tool *spawnSubTool) Name() string { return "spawn_sub" }
func (tool *spawnSubTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: tool.Name(), Description: "test", Parameters: map[string]any{"type": "object"}}
}
func (tool *spawnSubTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	subSess := newSession(tool.t.TempDir(), "s-sub")
	subReg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, tool.tracer)
	subEng := NewAgentEngine(subReg, &scriptedProvider{queue: []*schema.Message{
		{Role: schema.RoleAssistant, Content: "子结果", TokenUsed: schema.TokenStatistics{TokenInput: 10, TokenOutput: 5}},
	}}, tool.t.TempDir(), false, subSess, tool.tracer)
	subEng.Role = tracing.AgentRoleSub
	subEng.Printer = printer.DiscardPrinter{}
	return subEng.Run(ctx)
}
func (tool *spawnSubTool) BeforeExecInfo(json.RawMessage) string { return "spawn sub" }
func (tool *spawnSubTool) AfterExecInfo(json.RawMessage) string  { return "" }

// TestSubAgentNesting 验证子 Agent 子树嵌套在 tool-exec span 下，
// 与主 Agent 同一条 trace，agent_role 与 session_id 各自正确。
func TestSubAgentNesting(t *testing.T) {
	t.Parallel()

	mp := newMemProvider()
	reg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, mp.tracer)
	reg.Register(&spawnSubTool{tracer: mp.tracer, t: t})

	sp := &scriptedProvider{queue: []*schema.Message{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "spawn_sub", Arguments: mustJSON(t, map[string]any{})},
		}, TokenUsed: schema.TokenStatistics{TokenInput: 80, TokenOutput: 15}},
		{Role: schema.RoleAssistant, Content: "done", TokenUsed: schema.TokenStatistics{TokenInput: 40, TokenOutput: 8}},
	}}
	sess := newSession(t.TempDir(), "s-main")
	eng := newTracedEngine(t, mp, reg, sp, sess)

	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run 应成功: %v", err)
	}

	spans := mp.tracer.snapshot()
	runs := requireSpan(t, spans, tracing.SpanReAct, 2)
	toolSpans := requireSpan(t, spans, tracing.SpanToolExec, 1)
	subTool := toolSpans[0]

	var mainRun, subRun *memSpan
	for _, r := range runs {
		switch attrStr(r, tracing.AttrAgentRole) {
		case tracing.AgentRoleMain:
			mainRun = r
		case tracing.AgentRoleSub:
			subRun = r
		}
	}
	if mainRun == nil || subRun == nil {
		t.Fatalf("应各有一个 main/sub agent-run，实际 %+v", runs)
	}
	if subRun.parent != subTool {
		t.Error("子 agent-run 应嵌套在 tool-exec(spawn_sub) 下")
	}
	if subRun.traceID != mainRun.traceID {
		t.Error("子 Agent 应与主 Agent 同一条 trace")
	}
	if got := attrStr(subRun, tracing.AttrSessionID); got != "s-sub" {
		t.Errorf("子 run session_id = %q, want s-sub", got)
	}
	if got := attrStr(subTool, tracing.AttrSessionID); got != "s-main" {
		t.Errorf("tool-exec session_id = %q, want s-main", got)
	}
	// 子树内部完整：子 run 下有自己的 react-loop 与 llm-generate
	subLoops := spansNamed(spans, tracing.LLMTurn)
	found := false
	for _, l := range subLoops {
		if l.parent == subRun {
			found = true
		}
	}
	if !found {
		t.Error("子 agent-run 下应有自己的 react-loop")
	}
}

// TestToolErrorSpanStatus 验证工具失败只在 tool-exec 上置错误，
// 上层 react-loop / agent-run 不受影响（Agent 可基于错误修正后继续）。
func TestToolErrorSpanStatus(t *testing.T) {
	t.Parallel()

	mp := newMemProvider()
	reg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, mp.tracer)
	sp := &scriptedProvider{queue: []*schema.Message{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "no_such_tool", Arguments: mustJSON(t, map[string]any{})},
		}, TokenUsed: schema.TokenStatistics{TokenInput: 10, TokenOutput: 5}},
		{Role: schema.RoleAssistant, Content: "done", TokenUsed: schema.TokenStatistics{TokenInput: 10, TokenOutput: 5}},
	}}
	eng := newTracedEngine(t, mp, reg, sp, newSession(t.TempDir(), "s-err"))

	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("工具错误不应使 Run 失败: %v", err)
	}

	spans := mp.tracer.snapshot()
	toolSpan := requireSpan(t, spans, tracing.SpanToolExec, 1)[0]
	if toolSpan.status != codes.Error || toolSpan.errCnt == 0 {
		t.Error("tool-exec 应置错误状态并记录错误")
	}
	if run := requireSpan(t, spans, tracing.SpanReAct, 1)[0]; run.status == codes.Error {
		t.Error("工具可恢复错误不应使 agent-run 置错误")
	}
	for _, loop := range requireSpan(t, spans, tracing.LLMTurn, 2) {
		if loop.status == codes.Error {
			t.Error("工具可恢复错误不应使 react-loop 置错误")
		}
	}
}

// TestGenerateErrorSpanStatus 验证生成失败时 llm-generate 与 agent-run
// 均置错误状态。
func TestGenerateErrorSpanStatus(t *testing.T) {
	t.Parallel()

	mp := newMemProvider()
	reg := tools.NewDefaultRegistry(printer.DiscardPrinter{}, mp.tracer)
	p := &mockProvider{err: errors.New("api boom")}
	eng := newTracedEngine(t, mp, reg, p, newSession(t.TempDir(), "s-genfail"))

	if _, err := eng.Run(context.Background()); err == nil {
		t.Fatal("生成失败应使 Run 返回错误")
	}

	spans := mp.tracer.snapshot()
	gen := requireSpan(t, spans, tracing.SpanLLMGenerate, 1)[0]
	if gen.status != codes.Error || gen.errCnt == 0 {
		t.Error("llm-generate 应置错误状态并记录错误")
	}
	run := requireSpan(t, spans, tracing.SpanReAct, 1)[0]
	if run.status != codes.Error || run.errCnt == 0 {
		t.Error("agent-run 应置错误状态并记录错误")
	}
	if in, out := attrInt(run, tracing.AttrInputTokens), attrInt(run, tracing.AttrOutputTokens); in != 0 || out != 0 {
		t.Errorf("失败运行 token 合计应为 0/0，实际 %d/%d", in, out)
	}
	// 失败路径所有已开启 span 均应结束
	for _, s := range spans {
		if !s.ended {
			t.Errorf("span %q 未结束", s.name)
		}
	}
}

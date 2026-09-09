package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

// memRepo 是按 sessionID 分桶的内存 SessionRepository，测试用。
// 存储形态对齐 FsSessionRepo：系统提示词独立一份，GetMessages 时居首。
// fail* 开关用于验证仓储故障能沿 application 层透传，而不是被静默吞掉。
type memRepo struct {
	mu      sync.Mutex
	msgs    map[string][]sharedkernel.Message
	sysMsgs map[string]sharedkernel.Message
	metas   map[string]sharedkernel.SessionMeta

	failAppend      bool
	failUpsertSys   bool
	failUpdateMeta  bool
	failGetMessages bool
	failGetMeta     bool
}

// errRepo 是仓储故障的哨兵错误，供断言透传路径。
var errRepo = errors.New("repo failure")

func newMemRepo() *memRepo {
	return &memRepo{
		msgs:    make(map[string][]sharedkernel.Message),
		sysMsgs: make(map[string]sharedkernel.Message),
		metas:   make(map[string]sharedkernel.SessionMeta),
	}
}

func (m *memRepo) AppendMessage(_ context.Context, sessionID string, msg *sharedkernel.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAppend {
		return errRepo
	}
	m.msgs[sessionID] = append(m.msgs[sessionID], *msg)
	return nil
}

func (m *memRepo) UpsertSysMessage(_ context.Context, sessionID string, msg *sharedkernel.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failUpsertSys {
		return errRepo
	}
	m.sysMsgs[sessionID] = *msg
	return nil
}

func (m *memRepo) UpdateMeta(_ context.Context, sessionID string, meta *sharedkernel.SessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failUpdateMeta {
		return errRepo
	}
	m.metas[sessionID] = *meta
	return nil
}

func (m *memRepo) GetMessages(_ context.Context, sessionID string) ([]sharedkernel.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGetMessages {
		return nil, errRepo
	}
	var out []sharedkernel.Message
	if sys, ok := m.sysMsgs[sessionID]; ok {
		out = append(out, sys)
	}
	return append(out, m.msgs[sessionID]...), nil
}

func (m *memRepo) GetMeta(_ context.Context, sessionID string) (sharedkernel.SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGetMeta {
		return sharedkernel.SessionMeta{}, errRepo
	}
	return m.metas[sessionID], nil
}

// storedMsgs / storedMeta / storedSys 是断言用的读侧快照。
func (m *memRepo) storedMsgs(sessionID string) []sharedkernel.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.msgs[sessionID]
}

func (m *memRepo) storedSys(sessionID string) (sharedkernel.Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sys, ok := m.sysMsgs[sessionID]
	return sys, ok
}

func (m *memRepo) storedMeta(sessionID string) (sharedkernel.SessionMeta, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.metas[sessionID]
	return meta, ok
}

// scriptedLLM 按脚本依次返回 LLM 结果，用于无 API Key 驱动 ReAct 循环。
// lastMsgs 记录最近一次 Generate 实际收到的消息序列，供断言“发给模型前
// 已经压缩 / 系统提示词居首”。
type scriptedLLM struct {
	responses []scriptedResp
	calls     int
	lastMsgs  []sharedkernel.Message
}

type scriptedResp struct {
	msg *sharedkernel.Message
	err error
}

func (s *scriptedLLM) Generate(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	s.calls++
	s.lastMsgs = msgs
	if s.calls > len(s.responses) {
		// 超出脚本：返回无工具调用的空回答，避免死循环
		return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: ""}, nil
	}
	r := s.responses[s.calls-1]
	return r.msg, r.err
}

func (s *scriptedLLM) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition, emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	msg, err := s.Generate(ctx, msgs, tools)
	if err != nil {
		return nil, err
	}
	if msg.ReasoningContent != "" {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningStart})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningDelta, Delta: msg.ReasoningContent})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningEnd})
	}
	if msg.Content != "" {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextStart})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta, Delta: msg.Content})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextEnd})
	}
	for _, tc := range msg.ToolCalls {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkToolCall, ToolCall: &tc})
	}
	return msg, nil
}

func assistantMsg(content string) *sharedkernel.Message {
	return &sharedkernel.Message{
		Role:    sharedkernel.RoleAssistant,
		Content: content,
	}
}

func assistantMsgWithTool(toolCalls ...sharedkernel.ToolCall) *sharedkernel.Message {
	return &sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		ToolCalls: toolCalls,
	}
}

// eventRecorder 收集 ReAct 事件，供断言事件序列。
type eventRecorder struct {
	events []ReactEvent
}

func (e *eventRecorder) record(ev *ReactEvent) {
	e.events = append(e.events, *ev)
}

// echoTool 是最小 BaseTool 实现：回显入参。
type echoTool struct{}

func (echoTool) Name() string { return "echo_tool" }

func (echoTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{Name: "echo_tool", Description: "echo"}
}

func (echoTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a map[string]string
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	return fmt.Sprintf("echo:%s", a["msg"]), nil
}

func (echoTool) BeforeExecInfo(args json.RawMessage) string {
	return fmt.Sprintf("echo_tool(%s)", string(args))
}

func (echoTool) AfterExecInfo(json.RawMessage) string { return "" }

// fatalTool 是 Execute 恒返回错误的工具，用于验证 Registry 包装错误提示。
type fatalTool struct{}

func (fatalTool) Name() string { return "fatal_tool" }

func (fatalTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{Name: "fatal_tool", Description: "fatal"}
}

func (fatalTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("always fail")
}

func (fatalTool) BeforeExecInfo(json.RawMessage) string { return "fatal_tool()" }

func (fatalTool) AfterExecInfo(json.RawMessage) string { return "" }

// newTestSession 构造带内存 repo 的会话并写入一条系统提示，贴近真实装配：
// 聚合先落定状态并交出快照，再由调用方（平时是 ReActService.InitSysPrompt）落盘。
func newTestSession(id string, repo session.SessionRepository) *session.Session {
	sess := session.NewSession(id)
	sysMsg := sess.UpsertSysMessage("system prompt")
	if err := repo.UpsertSysMessage(context.Background(), id, &sysMsg); err != nil {
		panic(err)
	}
	return sess
}

// newTestService 按真实装配顺序构造服务：NewSession → NewReActService →
// InitSession → InitSysPrompt，供需要走完整生命周期的用例使用。
func newTestService(t *testing.T, id, sysPrompt string, repo session.SessionRepository,
	llm llmprovider.LLMClient, reg tools.Registry) *ReActService {
	t.Helper()
	svc := NewReActService(session.NewSession(id), repo, llm, reg, func(*ReactEvent) {}, nil)
	if err := svc.InitSession(context.Background()); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if err := svc.InitSysPrompt(context.Background(), sysPrompt); err != nil {
		t.Fatalf("InitSysPrompt: %v", err)
	}
	return svc
}

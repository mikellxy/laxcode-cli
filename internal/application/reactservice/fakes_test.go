package reactservice

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// memRepo 是按 sessionID 分桶的内存 SessionRepository，测试用。
type memRepo struct {
	mu    sync.Mutex
	msgs  map[string][]sharedkernel.Message
	metas map[string]*sharedkernel.SessionMeta
}

func newMemRepo() *memRepo {
	return &memRepo{
		msgs:  make(map[string][]sharedkernel.Message),
		metas: make(map[string]*sharedkernel.SessionMeta),
	}
}

func (m *memRepo) AppendMessage(_ context.Context, sessionID string, msg *sharedkernel.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs[sessionID] = append(m.msgs[sessionID], *msg)
	return nil
}

func (m *memRepo) UpdateMeta(_ context.Context, sessionID string, meta *sharedkernel.SessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metas[sessionID] = meta
	return nil
}

func (m *memRepo) GetMessages(_ context.Context, sessionID string) ([]sharedkernel.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.msgs[sessionID], nil
}

func (m *memRepo) GetMeta(_ context.Context, sessionID string) (*sharedkernel.SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta, nil
	}
	return new(sharedkernel.SessionMeta), nil
}

// scriptedLLM 按脚本依次返回 LLM 结果，用于无 API Key 驱动 ReAct 循环。
type scriptedLLM struct {
	responses []scriptedResp
	calls     int
}

type scriptedResp struct {
	msg *sharedkernel.Message
	err error
}

func (s *scriptedLLM) Generate(_ context.Context, _ []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	s.calls++
	if s.calls > len(s.responses) {
		// 超出脚本：返回无工具调用的空回答，避免死循环
		return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: ""}, nil
	}
	r := s.responses[s.calls-1]
	return r.msg, r.err
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

// newTestSession 构造带内存 repo 的会话并写入一条系统提示，贴近真实装配。
func newTestSession(id string, repo session.SessionRepository) *session.Session {
	sess := session.NewSession(id, repo)
	if err := sess.ReplaceSysPrompt(context.Background(), "system prompt"); err != nil {
		panic(err)
	}
	return sess
}

package reactservice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

// memRepo 是按 sessionID 分桶的内存 SessionRepository，测试用。
// contexts 模拟数据库工作集，msgs 保留原始历史供行为断言。
// fail* 开关用于验证仓储故障能沿 application 层透传，而不是被静默吞掉。
type memRepo struct {
	mu              sync.Mutex
	msgs            map[string][]sharedkernel.Message
	sysMsgs         map[string]sharedkernel.Message
	contexts        map[string]session.RequestContext
	artifacts       map[string]map[string]string
	failLoadContext bool
	failSaveContext bool
	failSnapshot    bool
	failAppend      bool
	failArtifact    bool
	createCalls     int
	updateCalls     int
	generationCalls int
	snapshotCalls   int // update + generation，保留给部分故障测试观察
	appendCalls     int
	lastSnapshot    session.RequestContext
	lastAppend      session.RequestContext
	lastAppendedMsg sharedkernel.Message
}

// errRepo 是仓储故障的哨兵错误，供断言透传路径。
var errRepo = errors.New("repo failure")

func newMemRepo() *memRepo {
	return &memRepo{
		msgs:      make(map[string][]sharedkernel.Message),
		sysMsgs:   make(map[string]sharedkernel.Message),
		contexts:  make(map[string]session.RequestContext),
		artifacts: make(map[string]map[string]string),
	}
}

func (m *memRepo) GetRequestContext(_ context.Context, id string) (session.RequestContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failLoadContext {
		return session.RequestContext{}, errRepo
	}
	if snapshot, ok := m.contexts[id]; ok {
		return snapshot.Clone(), nil
	}
	return session.RequestContext{MemoryGeneration: 1}, nil
}

func (m *memRepo) CommitCreateMessage(_ context.Context, id string, snapshot session.RequestContext, original, memory sharedkernel.Message) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	m.appendCalls++
	m.lastAppend = snapshot.Clone()
	m.lastAppendedMsg = original.Clone()
	previous, exists := m.contexts[id]
	if m.failSaveContext || m.failAppend {
		return 0, errRepo
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(original, memory) || len(snapshot.Messages) == 0 || !reflect.DeepEqual(snapshot.Messages[len(snapshot.Messages)-1], memory) {
		return 0, errRepo
	}
	if exists {
		if snapshot.Revision != previous.Revision || snapshot.LastSeq != previous.LastSeq+1 || snapshot.MemoryGeneration != previous.MemoryGeneration {
			return 0, errRepo
		}
	} else if snapshot.Revision != 0 || snapshot.LastSeq != 1 || snapshot.MemoryGeneration != 1 || len(snapshot.Messages) != 1 {
		return 0, errRepo
	}
	if original.Seq != snapshot.LastSeq || len(original.OriginalSeq) != 1 || original.OriginalSeq[0] != original.Seq {
		return 0, errRepo
	}
	m.msgs[id] = append(m.msgs[id], original.Clone())
	return m.saveSnapshot(id, snapshot), nil
}

func (m *memRepo) CommitUpdateMessage(_ context.Context, id string, snapshot session.RequestContext, memory sharedkernel.Message) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalls++
	m.snapshotCalls++
	m.lastSnapshot = snapshot.Clone()
	previous, exists := m.contexts[id]
	if m.failSaveContext || m.failSnapshot {
		return 0, errRepo
	}
	if !exists || snapshot.Revision != previous.Revision || snapshot.LastSeq != previous.LastSeq || snapshot.MemoryGeneration != previous.MemoryGeneration {
		return 0, errRepo
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	found := false
	for _, msg := range snapshot.Messages {
		if msg.Seq == memory.Seq && reflect.DeepEqual(msg, memory) {
			found = true
			break
		}
	}
	if !found {
		return 0, errRepo
	}
	return m.saveSnapshot(id, snapshot), nil
}

func (m *memRepo) CommitNextMemoryGeneration(_ context.Context, id string, snapshot session.RequestContext) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generationCalls++
	m.snapshotCalls++
	m.lastSnapshot = snapshot.Clone()
	previous, exists := m.contexts[id]
	if m.failSaveContext || m.failSnapshot {
		return 0, errRepo
	}
	if !exists || snapshot.Revision != previous.Revision || snapshot.LastSeq != previous.LastSeq || snapshot.MemoryGeneration != previous.MemoryGeneration+1 {
		return 0, errRepo
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	return m.saveSnapshot(id, snapshot), nil
}

func (m *memRepo) saveSnapshot(id string, snapshot session.RequestContext) uint64 {
	snapshot.Revision++
	m.contexts[id] = snapshot.Clone()
	if len(snapshot.Messages) > 0 && snapshot.Messages[0].Role == sharedkernel.RoleSystem {
		m.sysMsgs[id] = snapshot.Messages[0]
	}
	return snapshot.Revision
}

func (m *memRepo) PutArtifact(_ context.Context, sid, content string) (sharedkernel.ArtifactRef, error) {
	if m.failArtifact {
		return sharedkernel.ArtifactRef{}, errRepo
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	if m.artifacts[sid] == nil {
		m.artifacts[sid] = make(map[string]string)
	}
	m.artifacts[sid][id] = content
	return sharedkernel.ArtifactRef{ID: id, ByteSize: len(content)}, nil
}

func (m *memRepo) ReadArtifact(_ context.Context, sid, id string, offset, limit int) (tools.ArtifactPage, error) {
	content, ok := m.artifacts[sid][id]
	if !ok {
		return tools.ArtifactPage{}, errors.New("artifact missing")
	}
	runes := []rune(content)
	if offset < 0 || offset > len(runes) || limit < 1 {
		return tools.ArtifactPage{}, errors.New("invalid range")
	}
	end := min(len(runes), offset+limit)
	return tools.ArtifactPage{ID: id, Content: string(runes[offset:end]), Offset: offset, NextOffset: end, TotalRunes: len(runes), EOF: end == len(runes)}, nil
}

// storedMsgs / storedSys 是断言用的读侧快照。
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

// scriptedLLM 按脚本依次返回 LLM 结果，用于无 API Key 驱动 ReAct 循环。
// lastMsgs 记录最近一次 Generate 实际收到的消息序列，供断言“发给模型前
// 已经压缩 / 系统提示词居首”。
type scriptedLLM struct {
	responses  []scriptedResp
	calls      int
	lastMsgs   []sharedkernel.Message
	countCalls int
	countFn    func([]sharedkernel.Message, []sharedkernel.ToolDefinition) (int, error)
	budget     llmprovider.ContextBudget
}

type scriptedResp struct {
	msg *sharedkernel.Message
	err error
	// finishReason 直接写入返回消息的 FinishReason（不覆盖脚本 msg 里的
	// 显式取值），用于编排截断 / 取消等终止场景。
	finishReason string
}

func (s *scriptedLLM) Generate(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	s.calls++
	s.lastMsgs = msgs
	if s.calls > len(s.responses) {
		// 超出脚本：返回无工具调用的空回答，避免死循环
		return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: ""}, nil
	}
	r := s.responses[s.calls-1]
	if r.finishReason != "" && r.msg != nil {
		msg := r.msg.Clone()
		msg.FinishReason = r.finishReason
		return &msg, r.err
	}
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

func (s *scriptedLLM) CountInputTokens(_ context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition) (int, error) {
	s.countCalls++
	if s.countFn != nil {
		return s.countFn(msgs, defs)
	}
	count := 0
	for _, msg := range msgs {
		count += sharedkernel.EstimateTokenInt(msg.Content)
		count += sharedkernel.EstimateTokenInt(msg.ReasoningContent)
		for _, call := range msg.ToolCalls {
			count += sharedkernel.EstimateTokenInt(call.Name)
			count += sharedkernel.EstimateTokenInt(string(call.Arguments))
		}
	}
	for _, def := range defs {
		data, _ := json.Marshal(def)
		count += sharedkernel.EstimateTokenInt(string(data))
	}
	return count, nil
}

func (s *scriptedLLM) ContextBudget() llmprovider.ContextBudget {
	if s.budget.ContextWindow == 0 {
		return llmprovider.ContextBudget{ContextWindow: 200_000, ReservedOutputTokens: 20_000}
	}
	return s.budget
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
	sys := sess.UpsertSysMessage("system prompt")
	if revision, err := repo.CommitCreateMessage(context.Background(), id, sess.Snapshot(), sys, sys); err != nil {
		panic(err)
	} else {
		sess.Revision = revision
	}
	return sess
}

// newTestService 按真实装配顺序构造服务：NewSession → NewReActService →
// InitSession → InitSysPrompt，供需要走完整生命周期的用例使用。
func newTestService(t *testing.T, id, sysPrompt string, repo session.SessionRepository,
	llm llmprovider.LLMClient, reg tools.Registry) *ReActService {
	t.Helper()
	svc := NewReActService(session.NewSession(id), repo, llm, nil, reg, func(*ReactEvent) {}, nil)
	if err := svc.InitSession(context.Background()); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if err := svc.InitSysPrompt(context.Background(), sysPrompt); err != nil {
		t.Fatalf("InitSysPrompt: %v", err)
	}
	return svc
}

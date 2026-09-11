package reactservice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestNewReActService(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-main", repo)
	reg := tools.NewDefaultRegistry(nil)
	svc := NewReActService(sess, repo, &scriptedLLM{}, reg, nil, nil)
	if svc == nil {
		t.Fatal("NewReActService 返回 nil")
	}
	if svc.Session != sess || svc.ToolRegistry != reg || svc.SessRepo != repo {
		t.Error("构造参数未装配到服务")
	}
}

func TestRunReturnsImmediateAnswer(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-main", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsg("final answer")},
	}}
	rec := &eventRecorder{}
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), rec.record, nil)

	msg, err := svc.think(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msg == nil || msg.Content != "final answer" {
		t.Fatalf("应返回模型最终消息，实际 %+v", msg)
	}
	// 模型消息已追加进会话
	if len(sess.Messages) != 2 { // system + assistant
		t.Fatalf("会话应含 system+assistant 两条，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	if sess.Messages[1].Role != sharedkernel.RoleAssistant || sess.Messages[1].Content != "final answer" {
		t.Errorf("追加的消息不符：%+v", sess.Messages[1])
	}
	assertChunks(t, rec.events, []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "final answer"},
		{Kind: sharedkernel.ChunkTextEnd},
	})
}

func TestRunEmitsReasoningEvent(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-reason", repo)
	llm := &scriptedLLM{responses: []scriptedResp{{
		msg: &sharedkernel.Message{
			Role:             sharedkernel.RoleAssistant,
			Content:          "answer",
			ReasoningContent: "thinking",
		},
	}}}
	rec := &eventRecorder{}
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), rec.record, nil)

	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertChunks(t, rec.events, []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkReasoningStart},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "thinking"},
		{Kind: sharedkernel.ChunkReasoningEnd},
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "answer"},
		{Kind: sharedkernel.ChunkTextEnd},
	})
}

func TestRunToolCallLoop(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-tool", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(sharedkernel.ToolCall{
			ID:        "tc-1",
			Name:      "echo_tool",
			Arguments: []byte(`{"msg":"hi"}`),
		})},
		{msg: assistantMsg("done after tool")},
	}}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(echoTool{})
	rec := &eventRecorder{}
	svc := NewReActService(sess, repo, llm, reg, rec.record, nil)

	msg, err := svc.think(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msg.Content != "done after tool" {
		t.Fatalf("应返回工具循环后的最终消息，实际 %+v", msg)
	}
	if llm.calls != 2 {
		t.Errorf("应调用 LLM 两次，实际 %d", llm.calls)
	}
	// 会话：system + assistant(带工具调用) + tool + assistant(final)
	if len(sess.Messages) != 4 {
		t.Fatalf("会话应 4 条消息，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	toolMsg := sess.Messages[2]
	if toolMsg.Role != sharedkernel.RoleTool || toolMsg.ToolCallID != "tc-1" || toolMsg.Content != "echo:hi" {
		t.Errorf("工具结果消息不符：%+v", toolMsg)
	}
	// 事件流：tool_call 事件 + 最终正文
	var sawToolCall bool
	for _, e := range rec.events {
		if e.Type == ReActEventTypeToolCall {
			sawToolCall = true
			if !strings.Contains(e.Content, "echo_tool") {
				t.Errorf("tool_call 事件内容不符：%q", e.Content)
			}
		}
	}
	if !sawToolCall {
		t.Errorf("应推送 tool_call 事件，实际 %+v", rec.events)
	}
	if len(rec.events) != 5 || rec.events[0].ChunkEvent == nil ||
		!reflect.DeepEqual(*rec.events[0].ChunkEvent, sharedkernel.StreamChunk{
			Kind: sharedkernel.ChunkToolCall, ToolCall: &llm.responses[0].msg.ToolCalls[0],
		}) || rec.events[1].Type != ReActEventTypeToolCall {
		t.Errorf("完整工具调用 chunk 应先于执行提示，且正文不得重复推送：%+v", rec.events)
	}
}

func assertChunks(t *testing.T, events []ReactEvent, want []sharedkernel.StreamChunk) {
	t.Helper()
	var got []sharedkernel.StreamChunk
	for _, e := range events {
		if e.Type != ReActEventTypeChunk || e.ChunkEvent == nil {
			t.Fatalf("应仅包含 chunk 事件，实际 %+v", e)
		}
		got = append(got, *e.ChunkEvent)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunk 序列不符：got %+v, want %+v", got, want)
	}
}

type streamFunc func(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition, func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error)

func (f streamFunc) Generate(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	panic("ReAct 应调用 GenerateStream")
}

func (f streamFunc) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, emit func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	return f(ctx, msgs, defs, emit)
}

func (f streamFunc) CountInputTokens(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
	count := 0
	for _, msg := range msgs {
		count += sharedkernel.EstimateTokenInt(msg.Content)
		count += sharedkernel.EstimateTokenInt(msg.ReasoningContent)
	}
	return count, nil
}

func (f streamFunc) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 200_000, ReservedOutputTokens: 20_000}
}

func TestRunForwardsChunksBeforeStreamReturns(t *testing.T) {
	for _, streamErr := range []error{nil, errors.New("stream interrupted"), context.Canceled} {
		name := "success"
		if streamErr != nil {
			name = streamErr.Error()
		}
		t.Run(name, func(t *testing.T) {
			repo := newMemRepo()
			sess := newTestSession("s-stream", repo)
			rec := &eventRecorder{}
			chunks := []sharedkernel.StreamChunk{
				{Kind: sharedkernel.ChunkTextStart},
				{Kind: sharedkernel.ChunkTextDelta, Delta: "hel"},
				{Kind: sharedkernel.ChunkTextDelta, Delta: "lo"},
			}
			if streamErr == nil {
				chunks = append(chunks, sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextEnd})
			}
			llm := streamFunc(func(_ context.Context, _ []sharedkernel.Message, _ []sharedkernel.ToolDefinition, emit func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
				for i, chunk := range chunks {
					emit(chunk)
					assertChunks(t, rec.events, chunks[:i+1])
					if len(sess.Messages) != 1 || len(repo.storedMsgs(sess.ID)) != 0 {
						t.Fatal("生成完成前不得持久化或追加部分消息")
					}
				}
				if streamErr != nil {
					return nil, streamErr
				}
				return assistantMsg("hello"), nil
			})
			svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), rec.record, nil)
			msg, err := svc.think(context.Background())
			if !errors.Is(err, streamErr) {
				t.Fatalf("错误未透传：%v", err)
			}
			assertChunks(t, rec.events, chunks)
			if streamErr != nil {
				if msg != nil || len(sess.Messages) != 1 || len(repo.storedMsgs(sess.ID)) != 0 {
					t.Fatal("流式失败不得保存不完整回复")
				}
			} else if msg.Content != "hello" || len(sess.Messages) != 2 || len(repo.storedMsgs(sess.ID)) != 1 {
				t.Fatal("流式完成后应返回并保存一条完整回复")
			}
		})
	}
}

func TestRunPropagatesGenerateError(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-err", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{err: errors.New("llm down")},
	}}
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil)

	_, err := svc.think(context.Background())
	if err == nil || !strings.Contains(err.Error(), "llm down") {
		t.Fatalf("应透传 LLM 错误，实际 %v", err)
	}
	// 生成失败不追加任何消息
	if len(sess.Messages) != 1 {
		t.Errorf("失败轮不应追加消息，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
}

func TestRunUnknownToolDoesNotHang(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-ghost", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(sharedkernel.ToolCall{ID: "g1", Name: "ghost_tool", Arguments: []byte(`{}`)})},
		{msg: assistantMsg("recovered")},
	}}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(echoTool{}) // 不含 ghost_tool
	svc := NewReActService(sess, repo, llm, reg, func(*ReactEvent) {}, nil)

	msg, err := svc.think(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msg.Content != "recovered" {
		t.Fatalf("未知工具轮不应中断循环，实际 %+v", msg)
	}
	// 工具结果以 error 形式写入会话后循环继续
	if len(sess.Messages) != 4 {
		t.Errorf("会话应含未知工具的 error 结果消息，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	if !strings.Contains(sess.Messages[2].Content, "tool ghost_tool not exists") {
		t.Errorf("未知工具结果应说明原因：%q", sess.Messages[2].Content)
	}
}

func TestRunRegistersTokenUsageToSession(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-token", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: &sharedkernel.Message{
			Role:    sharedkernel.RoleAssistant,
			Content: "a",
			TokenUsed: sharedkernel.TokenStatistics{
				TokenInput:  100,
				TokenOutput: 20,
			},
		}},
	}}
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil, repo)
	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sess.TokenUsed.TokenInput != 100 || sess.TokenUsed.TokenOutput != 20 {
		t.Errorf("会话应累计 token 用量，实际 %+v", sess.TokenUsed)
	}
}

// ===== 会话生命周期与持久化编排（application 层职责） =====

func TestInitSessionRestoresHistoryAndMeta(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	sid := "s-resume"
	// 预置一份“上一次运行”留下的会话状态
	stored := session.NewSession(sid)
	stored.UpsertSysMessage("旧提示词")
	revision, err := repo.SaveCheckpoint(ctx, sid, stored.Snapshot())
	if err != nil {
		t.Fatalf("预置系统提示词：%v", err)
	}
	stored.Revision = revision
	userMsg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "上一轮提问"}
	if err := stored.AppendMessage(&userMsg); err != nil {
		t.Fatal(err)
	}
	stored.TokenUsed = sharedkernel.TokenStatistics{TokenInput: 300, TokenOutput: 40}
	stored.WindowToken = sharedkernel.TokenStatistics{TokenInput: 120, TokenOutput: 8}
	revision, err = repo.CommitAppendedMessage(ctx, sid, stored.Snapshot(), userMsg)
	if err != nil {
		t.Fatalf("预置工作集：%v", err)
	}
	stored.Revision = revision

	svc := NewReActService(session.NewSession(sid), repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)
	if err := svc.InitSession(ctx); err != nil {
		t.Fatalf("InitSession: %v", err)
	}

	sess := svc.Session
	if len(sess.Messages) != 2 {
		t.Fatalf("应读回 2 条历史，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	if sess.Messages[0].Role != sharedkernel.RoleSystem || sess.Messages[0].Content != "旧提示词" {
		t.Errorf("系统消息应居首，实际 %+v", sess.Messages[0])
	}
	if sess.Messages[1].Content != "上一轮提问" {
		t.Errorf("对话历史不符：%+v", sess.Messages[1])
	}
	if sess.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 300, TokenOutput: 40}) {
		t.Errorf("累计用量应从 meta 恢复，实际 %+v", sess.TokenUsed)
	}
	// WindowToken 恢复的是窗口占用，不是旧实现里的 TokenUsed
	if sess.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 120, TokenOutput: 8}) {
		t.Errorf("窗口占用应从 meta.WindowToken 恢复，实际 %+v", sess.WindowToken)
	}
}

func TestInitSessionPropagatesRepoError(t *testing.T) {
	ctx := context.Background()

	repo := newMemRepo()
	repo.failLoadContext = true
	svc := NewReActService(session.NewSession("s1"), repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)
	if err := svc.InitSession(ctx); !errors.Is(err, errRepo) {
		t.Errorf("GetRequestContext 失败应透传，实际 %v", err)
	}
}

func TestInitSysPromptWritesRepoAndKeepsSysAtHead(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, "s-sys", "旧提示词", repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil))

	// 再写一次：替换而非追加
	if err := svc.InitSysPrompt(context.Background(), "新提示词"); err != nil {
		t.Fatalf("InitSysPrompt: %v", err)
	}
	if len(svc.Session.Messages) != 1 || svc.Session.Messages[0].Content != "新提示词" {
		t.Errorf("聚合内应只保留最新系统提示词，实际 %+v", svc.Session.Messages)
	}

	sys, ok := repo.storedSys("s-sys")
	if !ok {
		t.Fatal("系统提示词应写入仓储")
	}
	if sys.Role != sharedkernel.RoleSystem || sys.Content != "新提示词" {
		t.Errorf("落盘的系统消息不符：%+v", sys)
	}
	// 估算占用不得写进 TokenUsed（那是模型返回的实测计费口径）
	if sys.TokenUsed != (sharedkernel.TokenStatistics{}) {
		t.Errorf("系统消息不应携带伪造的实测用量：%+v", sys.TokenUsed)
	}
	// 系统提示词不进只追加的对话流水
	if stored := repo.storedMsgs("s-sys"); len(stored) != 0 {
		t.Errorf("系统提示词不应写进 history，实际 %+v", stored)
	}
	if repo.checkpointCalls != 2 || repo.appendCalls != 0 {
		t.Fatalf("系统提示词应只走 checkpoint：checkpoint=%d append=%d", repo.checkpointCalls, repo.appendCalls)
	}
}

func TestInitSysPromptPropagatesRepoError(t *testing.T) {
	repo := newMemRepo()
	repo.failCheckpoint = true
	svc := NewReActService(session.NewSession("s1"), repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)
	if err := svc.InitSysPrompt(context.Background(), "p"); !errors.Is(err, errRepo) {
		t.Errorf("UpsertSysMessage 失败应透传，实际 %v", err)
	}
}

func TestChatAppendsUserMessageToRepoAndSession(t *testing.T) {
	repo := newMemRepo()
	llm := &scriptedLLM{responses: []scriptedResp{{msg: assistantMsg("ok")}}}
	svc := newTestService(t, "s-chat", "system prompt", repo, llm, tools.NewDefaultRegistry(nil))

	msg, err := svc.Chat(context.Background(), "你好")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if msg == nil || msg.Content != "ok" {
		t.Fatalf("应返回模型最终回答，实际 %+v", msg)
	}

	// 聚合：system + user + assistant
	sess := svc.Session
	if len(sess.Messages) != 3 {
		t.Fatalf("会话应含 3 条消息，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	if sess.Messages[1].Role != sharedkernel.RoleUser || sess.Messages[1].Content != "你好" {
		t.Errorf("用户消息不符：%+v", sess.Messages[1])
	}
	// 发给模型的序列同样以系统提示词居首
	if len(llm.lastMsgs) != 2 || llm.lastMsgs[0].Role != sharedkernel.RoleSystem {
		t.Errorf("发给模型的消息序列不符：%+v", llm.lastMsgs)
	}

	// 仓储：对话流水里有 user 与 assistant，系统提示词单独一份
	stored := repo.storedMsgs("s-chat")
	if len(stored) != 2 {
		t.Fatalf("history 应含 user+assistant 共 2 条，实际 %d：%+v", len(stored), stored)
	}
	if stored[0].Role != sharedkernel.RoleUser || stored[1].Role != sharedkernel.RoleAssistant {
		t.Errorf("history 落盘顺序不符：%+v", stored)
	}
	if repo.checkpointCalls != 1 || repo.appendCalls != 2 {
		t.Fatalf("Chat 应由一次初始化 checkpoint 和两次 append 组成：checkpoint=%d append=%d",
			repo.checkpointCalls, repo.appendCalls)
	}
	if sys, ok := repo.storedSys("s-chat"); !ok || sys.Content != "system prompt" {
		t.Errorf("系统提示词应独立落盘，实际 %+v / %v", sys, ok)
	}
}

func TestChatContinuesCommittedInterruptedInput(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	llm := &scriptedLLM{responses: []scriptedResp{{msg: assistantMsg("combined answer")}}}
	rec := &eventRecorder{}
	svc := newTestService(t, "s-recover-pending", "system prompt", repo, llm, tools.NewDefaultRegistry(nil))
	svc.ReActEventConsumerF = rec.record

	user := svc.Session.BuildUserMessage("old question")
	candidate, err := svc.Session.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAppendedMessage(ctx, candidate, user); err != nil {
		t.Fatal(err)
	}
	callMsg := assistantMsgWithTool(sharedkernel.ToolCall{ID: "call", Name: "side_effect"})
	if err := svc.handleTurnMsg(ctx, callMsg); err != nil {
		t.Fatal(err)
	}

	// 工具结果已经原子提交，但进程在模型收束前退出。
	committedResult := &sharedkernel.Message{
		Role: sharedkernel.RoleTool, ToolCallID: "call", Content: "uncertain result",
	}
	if err := svc.handleTurnMsg(ctx, committedResult); err != nil {
		t.Fatal(err)
	}
	// 模拟重启：新服务只从持久化工作集恢复。
	svc = NewReActService(session.NewSession("s-recover-pending"), repo, llm, tools.NewDefaultRegistry(nil), rec.record, nil)
	if err := svc.InitSession(ctx); err != nil {
		t.Fatal(err)
	}

	final, err := svc.Chat(ctx, "new question")
	if err != nil {
		t.Fatalf("恢复后 Chat: %v", err)
	}
	if final.Content != "combined answer" || llm.calls != 1 {
		t.Fatalf("旧轮次与新输入应合并为一次推理：final=%+v calls=%d", final, llm.calls)
	}
	msgs := svc.Session.Messages
	if len(msgs) != 6 || msgs[3].Role != sharedkernel.RoleTool || msgs[3].Content != "uncertain result" {
		t.Fatalf("已提交 tool result 应恢复且不重复补写：%+v", msgs)
	}
	if msgs[4].Role != sharedkernel.RoleUser || msgs[4].Content != "new question" ||
		msgs[5].Role != sharedkernel.RoleAssistant || msgs[5].Content != "combined answer" {
		t.Fatalf("恢复工具结果、新输入与回答顺序不符：%+v", msgs)
	}
	if len(llm.lastMsgs) != 5 || llm.lastMsgs[3].Role != sharedkernel.RoleTool ||
		llm.lastMsgs[4].Role != sharedkernel.RoleUser || llm.lastMsgs[4].Content != "new question" {
		t.Fatalf("模型应同时看到前滚结果和新输入：%+v", llm.lastMsgs)
	}
	if svc.Session.ActiveChatID != "" {
		t.Fatalf("两轮均完成后不应残留 ActiveChatID：%q", svc.Session.ActiveChatID)
	}
	if len(rec.events) == 0 || rec.events[0].Type != ReActEventTypeRecovery {
		t.Fatalf("应先发恢复事件，实际 %+v", rec.events)
	}
}

func TestChatSynthesizesOnlyMissingToolResults(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	llm := &scriptedLLM{responses: []scriptedResp{{msg: assistantMsg("combined done")}}}
	svc := newTestService(t, "s-recover-missing", "system prompt", repo, llm, tools.NewDefaultRegistry(nil))

	user := svc.Session.BuildUserMessage("old question")
	candidate, err := svc.Session.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAppendedMessage(ctx, candidate, user); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleTurnMsg(ctx, assistantMsgWithTool(
		sharedkernel.ToolCall{ID: "a", Name: "side_effect"},
		sharedkernel.ToolCall{ID: "b", Name: "side_effect"},
	)); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleTurnMsg(ctx, &sharedkernel.Message{
		Role: sharedkernel.RoleTool, ToolCallID: "a", Content: "known",
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟 Ctrl+C 后重新启动：新服务只从持久化快照恢复旧执行链。
	svc = newTestService(t, "s-recover-missing", "system prompt", repo, llm, tools.NewDefaultRegistry(nil))
	if svc.Session.ActiveChatID == "" {
		t.Fatal("重启后应保留未完成执行链的 ActiveChatID")
	}
	if _, err := svc.Chat(ctx, "new question"); err != nil {
		t.Fatal(err)
	}
	msgs := svc.Session.Messages
	if len(msgs) != 7 {
		t.Fatalf("消息数=%d：%+v", len(msgs), msgs)
	}
	synthetic := msgs[4]
	if synthetic.Role != sharedkernel.RoleTool || synthetic.ToolCallID != "b" || synthetic.Content != recoveryToolResultPrompt {
		t.Fatalf("只应为缺失的 b 构造恢复结果，实际 %+v", synthetic)
	}
	if synthetic.TurnID != msgs[2].TurnID || synthetic.ToolCallGroupID != msgs[2].ToolCallGroupID {
		t.Fatalf("synthetic tool result 未继承调用组标识：%+v / %+v", msgs[2], synthetic)
	}
	if msgs[5].Role != sharedkernel.RoleUser || msgs[5].Content != "new question" ||
		msgs[6].Role != sharedkernel.RoleAssistant || msgs[6].Content != "combined done" {
		t.Fatalf("新输入应紧跟在补齐的工具结果之后：%+v", msgs)
	}
	if llm.calls != 1 || len(llm.lastMsgs) != 6 ||
		llm.lastMsgs[4].Role != sharedkernel.RoleTool || llm.lastMsgs[4].ToolCallID != "b" ||
		llm.lastMsgs[5].Role != sharedkernel.RoleUser {
		t.Fatalf("模型请求应包含补齐结果和新输入：calls=%d msgs=%+v", llm.calls, llm.lastMsgs)
	}
}

func TestChatPersistsNewInputBeforeContinuingRecoveredChat(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	svc := newTestService(t, "s-recover-cancelled", "system prompt", repo,
		&scriptedLLM{}, tools.NewDefaultRegistry(nil))

	oldUser := svc.Session.BuildUserMessage("old question")
	candidate, err := svc.Session.WithStartedChat(&oldUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAppendedMessage(ctx, candidate, oldUser); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleTurnMsg(ctx, assistantMsgWithTool(
		sharedkernel.ToolCall{ID: "call", Name: "side_effect"},
	)); err != nil {
		t.Fatal(err)
	}

	// 重启后恢复缺失工具结果，但模型调用再次被 Ctrl+C 终止。
	llm := &scriptedLLM{responses: []scriptedResp{{err: context.Canceled}}}
	svc = newTestService(t, "s-recover-cancelled", "system prompt", repo, llm, tools.NewDefaultRegistry(nil))
	if _, err := svc.Chat(ctx, "changed requirement"); !errors.Is(err, context.Canceled) {
		t.Fatalf("模型取消错误应透传，实际 %v", err)
	}

	snapshot, err := repo.GetRequestContext(ctx, "s-recover-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveChatID != "chat-1" {
		t.Fatalf("再次中断后应保留原执行链，实际 %q", snapshot.ActiveChatID)
	}
	if len(snapshot.Messages) != 5 ||
		snapshot.Messages[3].Role != sharedkernel.RoleTool ||
		snapshot.Messages[3].ToolCallID != "call" ||
		snapshot.Messages[4].Role != sharedkernel.RoleUser ||
		snapshot.Messages[4].Content != "changed requirement" {
		t.Fatalf("新输入必须在模型调用前持久化：%+v", snapshot.Messages)
	}
}

func TestMissingToolResultsOnlyReturnsUncommittedCalls(t *testing.T) {
	messages := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Seq: 1},
		{Role: sharedkernel.RoleAssistant, Seq: 2, ToolCallGroupID: "current", ToolCalls: []sharedkernel.ToolCall{
			{ID: "a"}, {ID: "b"}, {ID: "c"},
		}},
		{Role: sharedkernel.RoleTool, Seq: 3, ToolCallGroupID: "current", ToolCallID: "b"},
		{Role: sharedkernel.RoleTool, Seq: 4, ToolCallGroupID: "current", ToolCallID: "a"},
		{Role: sharedkernel.RoleTool, Seq: 5, ToolCallGroupID: "old", ToolCallID: "c"},
	}
	missing := missingToolResults(messages)
	if len(missing) != 1 || missing[0].ID != "c" {
		t.Fatalf("只应补缺失的 c，实际 %+v", missing)
	}
}

// 先磁盘后内存：写盘失败时聚合状态不得变化，否则续聊读回的历史会与内存分叉。
func TestChatRepoFailureDoesNotMutateSession(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, "s-append-fail", "system prompt", repo,
		&scriptedLLM{responses: []scriptedResp{{msg: assistantMsg("never")}}}, tools.NewDefaultRegistry(nil))
	repo.failSaveContext = true // 装配完成后再注入故障

	before := len(svc.Session.Messages)
	if _, err := svc.Chat(context.Background(), "q"); !errors.Is(err, errRepo) {
		t.Fatalf("写盘失败应透传，实际 %v", err)
	}
	if len(svc.Session.Messages) != before {
		t.Errorf("写盘失败时聚合不应追加消息，实际 %d 条（原 %d）", len(svc.Session.Messages), before)
	}
}

func TestRunPersistsMetaAfterAssistantMessage(t *testing.T) {
	repo := newMemRepo()
	llm := &scriptedLLM{responses: []scriptedResp{{msg: &sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		Content:   "a",
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20},
	}}}}
	sess := newTestSession("s-meta", repo)
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil, repo)

	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("think: %v", err)
	}
	stored := repo.contexts["s-meta"]
	if stored.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20}) {
		t.Errorf("累计用量不符：%+v", stored.TokenUsed)
	}
	if stored.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20}) {
		t.Errorf("窗口占用应为本次实测值：%+v", stored.WindowToken)
	}
}

func TestRequestContextIncludesUserMessagesAndUsage(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	sess := newTestSession("s-persist", repo)
	svc := NewReActService(sess, repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)

	if err := svc.handleTurnMsg(ctx, &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "q"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.contexts[sess.ID].Messages) != 2 {
		t.Fatal("user message must checkpoint immediately")
	}
	if err := svc.handleTurnMsg(ctx, &sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 7, TokenOutput: 3},
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	stored := repo.contexts["s-persist"]
	if stored.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 7, TokenOutput: 3}) {
		t.Errorf("assistant 消息应落盘最新账目，实际 %+v", stored.TokenUsed)
	}
}

func TestRunPropagatesContextPersistError(t *testing.T) {
	repo := newMemRepo()
	llm := &scriptedLLM{responses: []scriptedResp{{msg: &sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 10, TokenOutput: 1},
	}}}}
	sess := newTestSession("s-meta-fail", repo)
	repo.failSaveContext = true
	svc := NewReActService(sess, repo, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil, repo)

	_, err := svc.think(context.Background())
	if !errors.Is(err, errRepo) {
		t.Fatalf("meta 落盘失败应透传，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "persist request context") {
		t.Errorf("错误应带上阶段信息便于定位，实际 %v", err)
	}
}

// provider 对完整下一请求的计数达到高水位时，应压缩并重计数。
func TestRunCompactsHistoryBeforeGenerate(t *testing.T) {
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	repo := newMemRepo()
	sess := newTestSession("s-compact", repo)
	svc := NewReActService(sess, repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil, repo)
	user := sess.BuildUserMessage("q")
	candidate, err := sess.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAppendedMessage(context.Background(), candidate, user); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []*sharedkernel.Message{
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "old", Name: "old-tool"}),
		{
			Role: sharedkernel.RoleTool, ToolCallID: "old", Content: strings.Repeat("工具输出", 2000),
		},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "m1", Name: "mid-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "m1", Content: "x1"},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "m2", Name: "mid-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "m2", Content: "x2"},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "latest", Name: "latest-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "latest", Content: "fresh"},
	} {
		if err := svc.handleTurnMsg(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	// 4 个工具调用轮次 > reActToolCallTurnKept(3)：最旧的 old span 落在最近窗口之外会被清理，
	// 最近 3 轮（m1/m2/latest）完整保留。

	llm := &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("done")}},
		budget:    llmprovider.ContextBudget{ContextWindow: 100, ReservedOutputTokens: 10},
		countFn: func(msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
			for _, msg := range msgs {
				if strings.Contains(msg.Content, "read_artifact") {
					return 50, nil
				}
			}
			return 80, nil
		},
	}
	svc.LLMClient = llm
	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("think: %v", err)
	}

	if len(llm.lastMsgs) != 10 {
		t.Fatalf("发给模型的消息数不符，实际 %d", len(llm.lastMsgs))
	}
	if !strings.Contains(llm.lastMsgs[3].Content, "read_artifact") {
		t.Errorf("早给模型前应清理早期超长工具输出，实际：%q", llm.lastMsgs[3].Content)
	}
	if llm.lastMsgs[9].Content != "fresh" {
		t.Fatal("最新工具 span 的结果不应随旧 span 被清理")
	}
	if !strings.Contains(sess.Messages[3].Content, "read_artifact") {
		t.Errorf("压缩结果应回写聚合，实际：%q", sess.Messages[3].Content[:40])
	}
	if llm.countCalls != 2 {
		t.Fatalf("压缩前后应各精确计数一次，实际 %d", llm.countCalls)
	}
	if sess.Messages[0].Role != sharedkernel.RoleSystem {
		t.Errorf("压缩不得弄丢系统提示词，实际首条：%+v", sess.Messages[0])
	}
	logOutput := logs.String()
	for _, want := range []string{
		`"msg":"context_compaction_triggered"`,
		`"before_input_tokens":80`,
		`"trigger_tokens":72`,
		`"target_tokens":54`,
		`"artifact_candidate_count":1`,
		`"msg":"context_compaction_completed"`,
		`"after_input_tokens":50`,
		`"exact_saved_tokens":30`,
		`"target_met":true`,
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("压缩日志缺少 %s：%s", want, logOutput)
		}
	}
}

func TestRunDoesNotGenerateWhenCompactionCannotReachExactTarget(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-compact-unreachable", repo)
	svc := NewReActService(sess, repo, &scriptedLLM{}, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil, repo)
	user := sess.BuildUserMessage("q")
	candidate, err := sess.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAppendedMessage(context.Background(), candidate, user); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []*sharedkernel.Message{
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "old", Name: "old-tool"}),
		{
			Role: sharedkernel.RoleTool, ToolCallID: "old", Content: strings.Repeat("large-output", 1000),
		},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "m1", Name: "mid-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "m1", Content: "x1"},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "m2", Name: "mid-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "m2", Content: "x2"},
		assistantMsgWithTool(sharedkernel.ToolCall{ID: "latest", Name: "latest-tool"}),
		{Role: sharedkernel.RoleTool, ToolCallID: "latest", Content: "fresh"},
	} {
		if err := svc.handleTurnMsg(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	// 4 个工具调用轮次 > reActToolCallTurnKept(3)：old span 会被清理（→ 70），
	// 但仍高于精确目标，且此后再无可节省项，压缩无法达标。

	llm := &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("must not be generated")}},
		budget:    llmprovider.ContextBudget{ContextWindow: 100, ReservedOutputTokens: 10},
		countFn: func(msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
			for _, msg := range msgs {
				if strings.Contains(msg.Content, "read_artifact") {
					return 70, nil
				}
			}
			return 80, nil
		},
	}
	svc.LLMClient = llm
	_, err = svc.think(context.Background())
	if !errors.Is(err, ErrContextTargetNotReach) {
		t.Fatalf("expected target-not-reached error, got %v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("generation must not run above exact target, calls=%d", llm.calls)
	}
}

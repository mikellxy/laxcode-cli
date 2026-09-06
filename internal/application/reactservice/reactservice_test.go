package reactservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestNewReActService(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-main", repo)
	reg := tools.NewDefaultRegistry(nil)
	svc := NewReActService(sess, &scriptedLLM{}, reg, nil, nil)
	if svc == nil {
		t.Fatal("NewReActService 返回 nil")
	}
	if svc.Session != sess || svc.ToolRegistry != reg {
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
	svc := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), rec.record, nil)

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
	// 事件：应推送正文消息事件
	var hasMsg bool
	for _, e := range rec.events {
		if e.Type == ReActEventTypeMsg && e.Content == "final answer" {
			hasMsg = true
		}
	}
	if !hasMsg {
		t.Errorf("应推送正文事件，实际 %+v", rec.events)
	}
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
	svc := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), rec.record, nil)

	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	types := map[string]string{}
	for _, e := range rec.events {
		types[e.Type] = e.Content
	}
	if types[ReActEventTypeReasoning] != "thinking" {
		t.Errorf("应推送 reasoning 事件，实际 %+v", rec.events)
	}
	if types[ReActEventTypeMsg] != "answer" {
		t.Errorf("应推送正文事件，实际 %+v", rec.events)
	}
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
	svc := NewReActService(sess, llm, reg, rec.record, nil)

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
}

func TestRunPropagatesGenerateError(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("s-err", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{err: errors.New("llm down")},
	}}
	svc := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil)

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
	svc := NewReActService(sess, llm, reg, func(*ReactEvent) {}, nil)

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
	svc := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), func(*ReactEvent) {}, nil)
	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sess.TokenUsed.TokenInput != 100 || sess.TokenUsed.TokenOutput != 20 {
		t.Errorf("会话应累计 token 用量，实际 %+v", sess.TokenUsed)
	}
}

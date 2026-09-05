package session

import (
	"context"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// memoryRepo 是 SessionRepository 的内存实现，测试用。
type memoryRepo struct {
	msgs []sharedkernel.Message
	meta *sharedkernel.SessionMeta
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{meta: new(sharedkernel.SessionMeta)}
}

func (m *memoryRepo) AppendMessage(_ context.Context, _ string, msg *sharedkernel.Message) error {
	m.msgs = append(m.msgs, *msg)
	return nil
}

func (m *memoryRepo) UpdateMeta(_ context.Context, _ string, meta *sharedkernel.SessionMeta) error {
	m.meta = meta
	return nil
}

func (m *memoryRepo) GetMessages(_ context.Context, _ string) ([]sharedkernel.Message, error) {
	return m.msgs, nil
}

func (m *memoryRepo) GetMeta(_ context.Context, _ string) (*sharedkernel.SessionMeta, error) {
	return m.meta, nil
}

func TestNewSessionDefaultID(t *testing.T) {
	s := NewSession("", nil)
	if s.ID == "" {
		t.Fatal("sessionID 为空时应生成默认时间戳 ID")
	}
	if s.Messages != nil {
		t.Errorf("新会话消息应为空，实际 %+v", s.Messages)
	}
	if s.Repo != nil {
		t.Errorf("传入 nil repo 应保持 nil")
	}
}

func TestNewSessionKeepsGivenID(t *testing.T) {
	s := NewSession("abc-123", nil)
	if s.ID != "abc-123" {
		t.Errorf("应保留传入的 ID，实际 %q", s.ID)
	}
}

func TestReplaceSysPrompt(t *testing.T) {
	ctx := context.Background()
	s := NewSession("s1", newMemoryRepo())

	// 空会话：追加 system 消息
	if err := s.ReplaceSysPrompt(ctx, "p1"); err != nil {
		t.Fatalf("ReplaceSysPrompt on empty: %v", err)
	}
	if len(s.Messages) != 1 || s.Messages[0].Role != sharedkernel.RoleSystem || s.Messages[0].Content != "p1" {
		t.Fatalf("首条应为 system/p1，实际 %+v", s.Messages)
	}

	// 已有首条 system：原地替换，不重复追加
	if err := s.ReplaceSysPrompt(ctx, "p2"); err != nil {
		t.Fatalf("ReplaceSysPrompt again: %v", err)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("替换 system 不应追加新消息，实际 %d 条", len(s.Messages))
	}
	if s.Messages[0].Content != "p2" {
		t.Errorf("system 内容应更新为 p2，实际 %q", s.Messages[0].Content)
	}
}

func TestAppendUserPrompt(t *testing.T) {
	ctx := context.Background()
	s := NewSession("s1", newMemoryRepo())
	if err := s.AppendUserPrompt(ctx, "hello"); err != nil {
		t.Fatalf("AppendUserPrompt: %v", err)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("应追加 1 条消息，实际 %d", len(s.Messages))
	}
	if s.Messages[0].Role != sharedkernel.RoleUser || s.Messages[0].Content != "hello" {
		t.Errorf("消息应为 user/hello，实际 %+v", s.Messages[0])
	}
}

func TestAppendMessageAssistantUpdatesCost(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo()
	s := NewSession("s1", repo)

	// 普通 user 消息：不计费、落盘并进入内存
	if err := s.AppendUserPrompt(ctx, "hi"); err != nil {
		t.Fatalf("append user: %v", err)
	}

	// assistant 消息携带 token 用量：累计到 TokenUsed/WindowToken
	msg := &sharedkernel.Message{
		Role:    sharedkernel.RoleAssistant,
		Content: "answer",
		TokenUsed: sharedkernel.TokenStatistics{
			TokenInput:  100,
			TokenOutput: 20,
		},
	}
	if err := s.AppendMessage(ctx, msg); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	if s.TokenUsed.TokenInput != 100 || s.TokenUsed.TokenOutput != 20 {
		t.Errorf("TokenUsed 未累计，实际 %+v", s.TokenUsed)
	}
	if s.WindowToken.TokenInput != 100 || s.WindowToken.TokenOutput != 20 {
		t.Errorf("WindowToken 未同步，实际 %+v", s.WindowToken)
	}
	// repo 侧消息与内存一致
	if len(repo.msgs) != 2 {
		t.Fatalf("repo 应收到 2 条消息，实际 %d", len(repo.msgs))
	}
	if repo.meta == nil || repo.meta.TokenUsed.TokenInput != 100 {
		t.Errorf("repo meta 应更新 token 用量，实际 %+v", repo.meta)
	}
}

func TestUpdateCostAndWindowToken(t *testing.T) {
	ctx := context.Background()
	s := NewSession("s1", newMemoryRepo())
	s.UpdateCost(ctx, 10, 5)
	if s.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 10, TokenOutput: 5}) ||
		s.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 10, TokenOutput: 5}) {
		t.Fatalf("UpdateCost 后状态错误：TokenUsed=%+v WindowToken=%+v", s.TokenUsed, s.WindowToken)
	}

	// 压缩回写窗口占用（负增量）
	s.UpdateWindowToken(ctx, -3, -2)
	if s.WindowToken.TokenInput != 7 || s.WindowToken.TokenOutput != 3 {
		t.Errorf("UpdateWindowToken 扣减后状态错误：%+v", s.WindowToken)
	}
	// UpdateCost 不叠加累计到 TokenUsed？不：UpdateCost 是累计赋值语义
	s.UpdateCost(ctx, 8, 4)
	if s.TokenUsed.TokenInput != 18 || s.TokenUsed.TokenOutput != 9 {
		t.Errorf("TokenUsed 应为累计值 18/9，实际 %+v", s.TokenUsed)
	}
	if s.WindowToken.TokenInput != 8 || s.WindowToken.TokenOutput != 4 {
		t.Errorf("UpdateCost 后 WindowToken 应等于本次用量 8/4，实际 %+v", s.WindowToken)
	}
}

func TestInitLoadsFromRepo(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo()
	s := NewSession("s1", repo)
	s.AppendUserPrompt(ctx, "q1")
	s.AppendMessage(ctx, &sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		Content:   "a1",
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 30, TokenOutput: 7},
	})

	// 模拟重启：新会话对象从 repo 恢复
	s2 := NewSession("s1", repo)
	if err := s2.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(s2.Messages) != 2 {
		t.Fatalf("Init 应加载 2 条历史，实际 %d", len(s2.Messages))
	}
	if s2.Messages[0].Role != sharedkernel.RoleUser || s2.Messages[1].Content != "a1" {
		t.Errorf("历史消息内容不符：%+v", s2.Messages)
	}
	if s2.TokenUsed.TokenInput != 30 || s2.TokenUsed.TokenOutput != 7 {
		t.Errorf("TokenUsed 应从 meta 恢复，实际 %+v", s2.TokenUsed)
	}
	if s2.WindowToken.TokenInput != 30 || s2.WindowToken.TokenOutput != 7 {
		t.Errorf("WindowToken 初始应等于 TokenUsed，实际 %+v", s2.WindowToken)
	}
}

// erroringRepo 在 AppendMessage 返回错误，用于验证 Session 把错误透传。
type erroringRepo struct{ memoryRepo }

func (m *erroringRepo) AppendMessage(_ context.Context, _ string, _ *sharedkernel.Message) error {
	return errAppend
}

var errAppend = &appendErr{}

type appendErr struct{}

func (*appendErr) Error() string { return "repo append failed" }

func TestAppendMessageRepoErrorPropagates(t *testing.T) {
	s := NewSession("s1", &erroringRepo{})
	msg := &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "x"}
	if err := s.AppendMessage(context.Background(), msg); err == nil {
		t.Fatal("repo 写入失败时应返回错误")
	} else if !strings.Contains(err.Error(), "append failed") {
		t.Errorf("应透传 repo 错误，实际 %v", err)
	}
	// 失败时内存中不应追加
	if len(s.Messages) != 0 {
		t.Errorf("repo 失败时内存不应追加，实际 %d 条", len(s.Messages))
	}
}

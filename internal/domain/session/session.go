package session

import (
	"context"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type Session struct {
	ID       string
	Messages []sharedkernel.Message
	// 会话累计 token 使用量
	TokenUsed sharedkernel.TokenStatistics
	// 窗口占用，发给 LLM 的 token 大小
	WindowToken sharedkernel.TokenStatistics
	Repo        SessionRepository
}

// NewSession 以 sessionID 新建空 Session；不创建任何目录或文件，
// 从未 Append 的空会话不会在磁盘留下痕迹。
func NewSession(sessionID string, repo SessionRepository) *Session {
	if sessionID == "" {
		sessionID = time.Now().Format("20060102-150405.000")
	}
	return &Session{
		ID:   sessionID,
		Repo: repo,
	}
}

// Init 初始化
// 1. 加载历史消息
func (s *Session) Init() error {
	msgs, err := s.Repo.GetMessages(context.Background(), s.ID)
	if err != nil {
		return err
	}
	s.Messages = msgs
	meta, err := s.Repo.GetMeta(context.Background(), s.ID)
	if err != nil {
		return err
	}
	s.TokenUsed = meta.TokenUsed
	s.WindowToken = meta.TokenUsed
	return nil
}

func (s *Session) ReplaceSysPrompt(ctx context.Context, p string) {
	if len(s.Messages) > 0 && s.Messages[0].Role == sharedkernel.RoleSystem {
		s.Messages[0].Content = p
		return
	}

	msg := sharedkernel.Message{
		Role:    sharedkernel.RoleSystem,
		Content: p,
	}
	msgs := make([]sharedkernel.Message, 0, len(s.Messages)+1)
	msgs = append(msgs, msg)
	msgs = append(msgs, s.Messages...)
	s.Messages = msgs
}

func (s *Session) AppendUserPrompt(ctx context.Context, p string) error {
	msg := sharedkernel.Message{
		Role:    sharedkernel.RoleUser,
		Content: p,
	}
	return s.AppendMessage(ctx, &msg)
}

func (s *Session) AppendMessage(ctx context.Context, msg *sharedkernel.Message) error {
	if err := s.Repo.AppendMessage(ctx, s.ID, msg); err != nil {
		return err
	}
	s.Messages = append(s.Messages, *msg)
	return nil
}

func (s *Session) UpdateCost(tokenInput, tokenOutput int) {
	s.TokenUsed.TokenInput += tokenInput
	s.TokenUsed.TokenOutput += tokenOutput
}

func (s *Session) UpdateWindowToken(tokenInput, tokenOutput int) {
	s.WindowToken.TokenInput = tokenInput
	s.WindowToken.TokenOutput = tokenOutput
}

func (s *Session) UpdateMeta(ctx context.Context) {
	meta := &sharedkernel.SessionMeta{
		TokenUsed:   s.TokenUsed,
		WindowToken: s.WindowToken,
	}
	if err := s.Repo.UpdateMeta(ctx, s.ID, meta); err != nil {
	}
}

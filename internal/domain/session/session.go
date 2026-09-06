package session

import (
	"context"
	"errors"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/utils"
)

type Session struct {
	ID string
	// 系统提示词，Messages列表首位
	SysMessage *sharedkernel.Message
	Messages   []sharedkernel.Message
	// 会话累计 token 使用量
	TokenUsed sharedkernel.TokenStatistics
	// 窗口占用，发给 LLM 的 token 大小
	WindowToken sharedkernel.TokenStatistics
}

// NewSession 以 sessionID 新建空 Session；不创建任何目录或文件，
// 从未 Append 的空会话不会在磁盘留下痕迹。
func NewSession(sessionID string) *Session {
	if sessionID == "" {
		sessionID = time.Now().Format("20060102-150405.000")
	}
	return &Session{
		ID: sessionID,
	}
}

func (s *Session) LoadMessages(ctx context.Context, msgs []sharedkernel.Message) {
	if len(msgs) > 0 && msgs[0].Role == sharedkernel.RoleSystem {
		s.SysMessage = &msgs[0]
	}
	s.Messages = msgs
}

func (s *Session) LoadMeta(ctx context.Context, meta sharedkernel.SessionMeta) {
	s.TokenUsed.OverWrite(meta.TokenUsed)
	s.WindowToken.OverWrite(meta.WindowToken)
}

func (s *Session) LoadCompactorResult(ctx context.Context, msgs []sharedkernel.Message,
	compressedToken sharedkernel.TokenStatistics) {
	s.LoadMessages(ctx, msgs)
	s.WindowToken.Minus(compressedToken)
}

func (s *Session) BuildSysMessage(ctx context.Context, content string) *sharedkernel.Message {
	n := utils.EstimateTokenInt(content)
	return &sharedkernel.Message{
		Role:    sharedkernel.RoleSystem,
		Content: content,
		TokenUsed: sharedkernel.TokenStatistics{
			TokenInput: n,
		},
	}
}

func (s *Session) BuildUserMessage(ctx context.Context, content string) *sharedkernel.Message {
	return &sharedkernel.Message{
		Role:    sharedkernel.RoleUser,
		Content: content,
	}
}

func (s *Session) UpsertSysMessage(ctx context.Context, sysMsg *sharedkernel.Message) error {
	if sysMsg == nil || sysMsg.Role != sharedkernel.RoleSystem {
		return errors.New("nil or not a system message")
	}

	if len(s.Messages) == 0 {
		s.Messages = []sharedkernel.Message{*sysMsg}
	} else if s.Messages[0].Role == sharedkernel.RoleSystem {
		s.Messages[0] = *sysMsg
	} else {
		msgs := make([]sharedkernel.Message, 0, len(s.Messages)+1)
		msgs = append(msgs, *sysMsg)
		msgs = append(msgs, s.Messages...)
		s.Messages = msgs
	}

	if s.SysMessage != nil {
		s.WindowToken.Minus(s.SysMessage.TokenUsed)
	}
	s.WindowToken.Add(sysMsg.TokenUsed)
	s.SysMessage = sysMsg

	return nil
}

func (s *Session) AppendMessage(ctx context.Context, msg *sharedkernel.Message) error {
	if msg == nil {
		return errors.New("nil message")
	}

	if msg.Role == sharedkernel.RoleAssistant {
		s.WindowToken.OverWrite(msg.TokenUsed)
		s.TokenUsed.Add(msg.TokenUsed)
	}

	s.Messages = append(s.Messages, *msg)

	return nil
}

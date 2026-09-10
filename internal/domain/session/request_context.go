package session

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const RequestContextVersion = 1

// RequestContext 是会话最新工作集；完整历史仅在仓储追加保存。
// LastSeq 不因压缩改变，避免重启后复用历史消息的标识。
type RequestContext struct {
	Version      int                          `json:"version"`
	LastSeq      uint64                       `json:"last_seq"`
	ActiveChatID string                       `json:"active_chat_id,omitempty"`
	Messages     []sharedkernel.Message       `json:"messages"`
	TokenUsed    sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken  sharedkernel.TokenStatistics `json:"window_token"`
}

func (r RequestContext) Clone() RequestContext {
	r.Messages = sharedkernel.CloneMessages(r.Messages)
	return r
}

func (r RequestContext) Validate() error {
	if r.Version != RequestContextVersion {
		return fmt.Errorf("session: unsupported request context version %d", r.Version)
	}
	var prev uint64
	for i, m := range r.Messages {
		if m.Role == sharedkernel.RoleSystem {
			if i != 0 || m.Seq != 0 {
				return fmt.Errorf("session: invalid system message position/seq")
			}
			continue
		}
		if m.Seq <= prev || m.Seq > r.LastSeq {
			return fmt.Errorf("session: invalid message sequence %d", m.Seq)
		}
		prev = m.Seq
	}
	return nil
}

// inferActiveChatID 兼容尚未写入 ActiveChatID 的 v1 快照。只有尾消息明确
// 表示 ReAct 尚未收束时才推断为活跃；普通 assistant 尾消息视为已完成。
func (r *RequestContext) inferActiveChatID() {
	if r.ActiveChatID != "" || len(r.Messages) == 0 {
		return
	}
	tail := r.Messages[len(r.Messages)-1]
	if tail.Role == sharedkernel.RoleSystem ||
		(tail.Role == sharedkernel.RoleAssistant && len(tail.ToolCalls) == 0) {
		return
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == sharedkernel.RoleUser {
			r.ActiveChatID = fmt.Sprintf("chat-%d", r.Messages[i].Seq)
			return
		}
	}
}

func (s *Session) Snapshot() RequestContext { return s.RequestContext.Clone() }

func (s *Session) Restore(snapshot RequestContext) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	snapshot.inferActiveChatID()
	s.RequestContext = snapshot.Clone()
	s.refreshSysToken()
	return nil
}

func (s *Session) Clone() *Session {
	out := *s
	out.RequestContext = s.Snapshot()
	return &out
}

// WithAppendedMessage 构造只用于追加提交的候选。复制消息切片并预留一格，
// 旧消息的 ToolCalls、Arguments、Artifact 按只读数据共享；新增消息由
// AppendMessage 隔离。候选及提交调用都不得修改旧消息的嵌套字段。
// 需要修改历史（例如压缩）时，使用 Clone 获取完全隔离的副本。
func (s *Session) WithAppendedMessage(msg *sharedkernel.Message) (*Session, error) {
	candidate := *s
	candidate.Messages = make([]sharedkernel.Message, len(s.Messages), len(s.Messages)+1)
	copy(candidate.Messages, s.Messages)
	if err := candidate.AppendMessage(msg); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// WithStartedChat 原子构造“追加用户消息 + 标记活跃对话”的候选状态。
// ActiveChatID 由用户消息稳定 Seq 派生，与候选快照一起持久化。
func (s *Session) WithStartedChat(msg *sharedkernel.Message) (*Session, error) {
	if s.ActiveChatID != "" {
		return nil, ErrChatAlreadyActive
	}
	if msg == nil {
		return nil, ErrNilMessage
	}
	if msg.Role != sharedkernel.RoleUser {
		return nil, ErrStartChatRole
	}
	candidate, err := s.WithAppendedMessage(msg)
	if err != nil {
		return nil, err
	}
	candidate.ActiveChatID = fmt.Sprintf("chat-%d", msg.Seq)
	return candidate, nil
}

// identify 在写原文之前赋值，工具结果从其调用消息继承标识。
// ID 由稳定 Seq 派生，无需另一个需要落盘的计数器。
func (s *Session) identify(msg *sharedkernel.Message) error {
	if msg.Seq == 0 {
		msg.Seq = s.LastSeq + 1
	}
	if msg.Seq <= s.LastSeq {
		return fmt.Errorf("session: sequence %d is not after %d", msg.Seq, s.LastSeq)
	}
	switch msg.Role {
	case sharedkernel.RoleAssistant:
		msg.TurnID = fmt.Sprintf("turn-%d", msg.Seq)
		msg.ToolCallGroupID = ""
		if len(msg.ToolCalls) > 0 {
			msg.ToolCallGroupID = fmt.Sprintf("tool-group-%d", msg.Seq)
		}
	case sharedkernel.RoleTool:
		msg.TurnID, msg.ToolCallGroupID = "", ""
		for i := len(s.Messages) - 1; i >= 0; i-- {
			for _, call := range s.Messages[i].ToolCalls {
				if call.ID == msg.ToolCallID {
					msg.TurnID = s.Messages[i].TurnID
					msg.ToolCallGroupID = s.Messages[i].ToolCallGroupID
					return nil
				}
			}
		}
	case sharedkernel.RoleUser:
		msg.TurnID, msg.ToolCallGroupID = "", ""
	}
	return nil
}

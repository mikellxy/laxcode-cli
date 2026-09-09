package session

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const RequestContextVersion = 1

// RequestContext 是会话最新工作集；完整历史仅在仓储追加保存。
// LastSeq 不因压缩改变，避免重启后复用历史消息的标识。
type RequestContext struct {
	Version     int                          `json:"version"`
	LastSeq     uint64                       `json:"last_seq"`
	Messages    []sharedkernel.Message       `json:"messages"`
	TokenUsed   sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken sharedkernel.TokenStatistics `json:"window_token"`
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

func (s *Session) Snapshot() RequestContext { return s.RequestContext.Clone() }

func (s *Session) Restore(snapshot RequestContext) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
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

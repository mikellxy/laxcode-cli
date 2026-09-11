package session

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// RequestContext 是会话最新工作集；完整历史仅在仓储追加保存。
// LastSeq 不因压缩改变，避免重启后复用历史消息的标识。
type RequestContext struct {
	// Revision 是仓储乐观锁版本，不参与 JSON 冷备；首次保存为 0，每次数据库
	// 提交成功后加一。
	Revision         uint64                 `json:"-"`
	MemoryGeneration uint64                 `json:"memory_generation"`
	LastSeq          uint64                 `json:"last_seq"`
	Messages         []sharedkernel.Message `json:"messages"`
	// TokenUsed 累计正常 assistant 生成与上下文摘要生成的 provider 实测用量。
	TokenUsed   sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken sharedkernel.TokenStatistics `json:"window_token"`
}

func (r RequestContext) Clone() RequestContext {
	r.Messages = sharedkernel.CloneMessages(r.Messages)
	return r
}

func (r RequestContext) Validate() error {
	if r.MemoryGeneration == 0 {
		return fmt.Errorf("session: memory generation must start at 1")
	}
	if len(r.Messages) == 0 {
		if r.LastSeq != 0 {
			return fmt.Errorf("session: empty context has last sequence %d", r.LastSeq)
		}
		return nil
	}
	var prev uint64
	for i, m := range r.Messages {
		if i == 0 && m.Role != sharedkernel.RoleSystem {
			return fmt.Errorf("session: first message must be system")
		}
		if i > 0 && m.Role == sharedkernel.RoleSystem {
			return fmt.Errorf("session: system message must be first")
		}
		if m.Seq == 0 || m.Seq <= prev || m.Seq > r.LastSeq {
			return fmt.Errorf("session: invalid message sequence %d", m.Seq)
		}
		if err := validateOriginalSeq(m, r.LastSeq); err != nil {
			return err
		}
		prev = m.Seq
	}
	if prev != r.LastSeq {
		return fmt.Errorf("session: last message sequence %d does not match context %d", prev, r.LastSeq)
	}
	return nil
}

func validateOriginalSeq(m sharedkernel.Message, lastSeq uint64) error {
	if len(m.OriginalSeq) == 0 || m.OriginalSeq[0] != m.Seq {
		return fmt.Errorf("session: message %d has invalid original sequences %v", m.Seq, m.OriginalSeq)
	}
	var previous uint64
	for _, originalSeq := range m.OriginalSeq {
		if originalSeq == 0 || originalSeq > lastSeq || originalSeq <= previous {
			return fmt.Errorf("session: message %d has invalid original sequences %v", m.Seq, m.OriginalSeq)
		}
		previous = originalSeq
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

// identify 在写原文之前由聚合分配序号。Application 只表达新增消息，不接触
// 发号细节；新创建的原始消息只映射到自身。摘要消息不走 AppendMessage，
// 而是在压缩候选中由 compactor 原子替换一段既有工作集。
func (s *Session) identify(msg *sharedkernel.Message) error {
	if msg.Seq == 0 {
		seq, err := s.nextSeq()
		if err != nil {
			return err
		}
		msg.Seq = seq
	}
	if msg.Seq <= s.LastSeq {
		return fmt.Errorf("session: sequence %d is not after %d", msg.Seq, s.LastSeq)
	}
	if len(msg.OriginalSeq) == 0 {
		msg.OriginalSeq = []uint64{msg.Seq}
	}
	if len(msg.OriginalSeq) != 1 || msg.OriginalSeq[0] != msg.Seq {
		return fmt.Errorf("session: original sequences %v do not identify new sequence %d", msg.OriginalSeq, msg.Seq)
	}
	return nil
}

func (s *Session) nextSeq() (uint64, error) {
	if s.LastSeq == ^uint64(0) {
		return 0, fmt.Errorf("session: message sequence exhausted")
	}
	return s.LastSeq + 1, nil
}

// AdvanceMemoryGeneration 在一整轮压缩确认成功后调用一次。多轮压缩策略
// 共用同一个候选 generation，避免一次压缩产生多个版本。
func (s *Session) AdvanceMemoryGeneration() error {
	if s.MemoryGeneration == ^uint64(0) {
		return fmt.Errorf("session: memory generation exhausted")
	}
	s.MemoryGeneration++
	return nil
}

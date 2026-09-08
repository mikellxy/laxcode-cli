// Package session 承载会话聚合：消息序列（系统提示词恒居首）、token 账目
// （累计用量 / 窗口占用）与上下文压缩的落地。
//
// 聚合只做内存内的状态演化：不持有仓储、不触任何 I/O。加载与落盘由
// application 层经 SessionRepository 端口编排（domain 定义端口、
// infrastructure/sessionrepo 实现、cmd/agentasm 装配），因此本包的方法
// 一律不接 context.Context——ctx 是 I/O 取消与追踪传播的载体，纯领域演化
// 用不到它。
package session

import (
	"errors"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// 聚合不变量被破坏时返回的错误。调用方（application 层）据此判定是编程
// 失误还是可恢复故障，故用哨兵值而非就地 errors.New，便于 errors.Is 断言。
var (
	// ErrNilMessage 表示传入了 nil 消息指针。
	ErrNilMessage = errors.New("session: nil message")
	// ErrSystemViaAppend 表示试图经 AppendMessage 写入系统消息：系统提示词
	// 恒居 Messages 首位且独立落盘，只能走 UpsertSysMessage。
	ErrSystemViaAppend = errors.New("session: system message must go through UpsertSysMessage")
	// ErrNilCompactor 表示未注入压缩策略。
	ErrNilCompactor = errors.New("session: nil compactor strategy")
)

type Session struct {
	ID string
	// Messages 是发给 LLM 的消息序列；系统提示词（若已设置）恒为首元素。
	Messages []sharedkernel.Message
	// 会话累计 token 使用量
	TokenUsed sharedkernel.TokenStatistics
	// 窗口占用，发给 LLM 的 token 大小
	WindowToken sharedkernel.TokenStatistics
	// sysToken 是当前系统提示词的本地估算占用，仅用于替换提示词时校正
	// WindowToken（扣旧加新）。刻意不导出、也不写进 Message.TokenUsed：
	// 那是模型返回的实测计费口径，混入估算值会污染落盘的历史。
	sysToken int
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

// LoadMessages 用仓储读回的历史重建消息序列（装配 / 续聊期调用一次）。
// 首元素为系统消息时认领其估算占用，否则清零——避免留下上一次加载的悬挂值。
func (s *Session) LoadMessages(msgs []sharedkernel.Message) {
	s.Messages = msgs
	s.sysToken = 0
	if len(msgs) > 0 && msgs[0].Role == sharedkernel.RoleSystem {
		s.sysToken = sharedkernel.EstimateTokenInt(msgs[0].Content)
	}
}

// LoadMeta 用仓储读回的 meta 重建 token 账目（装配 / 续聊期调用一次）。
func (s *Session) LoadMeta(meta sharedkernel.SessionMeta) {
	s.TokenUsed.OverWrite(meta.TokenUsed)
	s.WindowToken.OverWrite(meta.WindowToken)
}

// Meta 返回当前 token 账目快照，供 application 层落盘（meta.json）。
func (s *Session) Meta() sharedkernel.SessionMeta {
	return sharedkernel.SessionMeta{
		TokenUsed:   s.TokenUsed,
		WindowToken: s.WindowToken,
	}
}

// UpsertSysMessage 以 content 替换（缺失时插入）系统提示词，返回写入后的
// 消息快照供 application 层落盘。
//
// 不变量：系统提示词恒为 Messages 首位，重复调用只替换不追加；聚合内部
// 只保存副本，返回值与内部状态互不别名，调用方改动返回值不会影响会话。
// 窗口占用同步校正：扣掉旧提示词的估算占用，加上新提示词的。
func (s *Session) UpsertSysMessage(content string) sharedkernel.Message {
	sysMsg := sharedkernel.Message{
		Role:    sharedkernel.RoleSystem,
		Content: content,
	}

	if len(s.Messages) == 0 {
		s.Messages = []sharedkernel.Message{sysMsg}
	} else if s.Messages[0].Role == sharedkernel.RoleSystem {
		s.Messages[0] = sysMsg
	} else {
		msgs := make([]sharedkernel.Message, 0, len(s.Messages)+1)
		msgs = append(msgs, sysMsg)
		msgs = append(msgs, s.Messages...)
		s.Messages = msgs
	}

	newToken := sharedkernel.EstimateTokenInt(content)
	s.WindowToken.Minus(sharedkernel.TokenStatistics{TokenInput: s.sysToken})
	s.WindowToken.Add(sharedkernel.TokenStatistics{TokenInput: newToken})
	s.sysToken = newToken

	return sysMsg
}

// BuildUserMessage 构造一条用户消息（不落盘、不入序列），由 application 层
// 先写仓储再交 AppendMessage。
func (s *Session) BuildUserMessage(content string) sharedkernel.Message {
	return sharedkernel.Message{
		Role:    sharedkernel.RoleUser,
		Content: content,
	}
}

// AppendMessage 把一条模型 / 用户 / 工具消息追加进序列，并结算 token 账目：
// 只有 assistant 消息携带模型返回的实测用量，故累计用量只在此增长，窗口占用
// 以本次实测值整体覆盖（实测输入本就包含系统提示词与当时全部历史）。
//
// 系统消息被拒（见 ErrSystemViaAppend）：它恒居首位且独立落盘，若混进追加
// 路径，续聊时就会出现两条系统提示词一起发给模型。
func (s *Session) AppendMessage(msg *sharedkernel.Message) error {
	if msg == nil {
		return ErrNilMessage
	}
	if msg.Role == sharedkernel.RoleSystem {
		return ErrSystemViaAppend
	}

	if msg.Role == sharedkernel.RoleAssistant {
		s.WindowToken.OverWrite(msg.TokenUsed)
		s.TokenUsed.Add(msg.TokenUsed)
	}

	s.Messages = append(s.Messages, *msg)

	return nil
}

// Compactor 是上下文压缩端口的消费者侧定义：接口由使用方（本包）声明，
// domain/compactor 的实现按 Go 惯例结构化匹配，无需相互 import——
// Compress 的签名全部由 sharedkernel 类型构成，天然可隐式满足。
type Compactor interface {
	Compress(msgs []sharedkernel.Message, maxToken int, winConsumed sharedkernel.TokenStatistics) ([]sharedkernel.Message, sharedkernel.TokenStatistics, error)
}

// Compact 按窗口预算压缩历史：策略在聚合内部改写消息序列，节省量同步从
// WindowToken 扣除。触发判据用 WindowToken（当前窗口占用）而非 TokenUsed
// （会话累计，只增不减）——后者会让长会话每轮都误触发压缩。
//
// 压缩结果只在内存生效：history.jsonl 始终保留完整原文，续聊后按原文重新
// 压缩，故落盘的 meta 也不记压缩后的窗口值。
func (s *Session) Compact(strategy Compactor, maxToken int) error {
	if strategy == nil {
		return ErrNilCompactor
	}
	msgs, saved, err := strategy.Compress(s.Messages, maxToken, s.WindowToken)
	if err != nil {
		return err
	}
	s.LoadMessages(msgs)
	s.WindowToken.Minus(saved)
	return nil
}

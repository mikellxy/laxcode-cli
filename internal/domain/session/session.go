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
	// ErrChatAlreadyActive 表示上一条用户请求尚未完成，不能直接开始下一条。
	ErrChatAlreadyActive = errors.New("session: previous chat is still active")
	// ErrStartChatRole 表示启动对话时传入的不是 user 消息。
	ErrStartChatRole = errors.New("session: chat must start with a user message")
)

type Session struct {
	ID string
	// 嵌入保持 Messages/TokenUsed 等读侧访问兼容，内存只有一份工作集。
	RequestContext
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
		ID:             sessionID,
		RequestContext: RequestContext{Version: RequestContextVersion},
	}
}

func (s *Session) refreshSysToken() {
	s.sysToken = 0
	if len(s.Messages) > 0 && s.Messages[0].Role == sharedkernel.RoleSystem {
		s.sysToken = sharedkernel.EstimateTokenInt(s.Messages[0].Content)
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
// 在候选 Session 追加后，将原始消息和候选快照一起交给仓储。
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
	if err := s.identify(msg); err != nil {
		return err
	}

	if msg.Role == sharedkernel.RoleAssistant {
		s.WindowToken.OverWrite(msg.TokenUsed)
		s.TokenUsed.Add(msg.TokenUsed)
		if len(msg.ToolCalls) == 0 {
			s.ActiveChatID = ""
		}
	}

	s.Messages = append(s.Messages, msg.Clone())
	s.LastSeq = msg.Seq

	return nil
}

// Compactor 是上下文压缩端口的消费者侧定义：接口由使用方（本包）声明，
// domain/compactor 的实现按 Go 惯例结构化匹配，无需相互 import——
// Compress 的签名全部由 sharedkernel 类型构成，天然可隐式满足。
type Compactor interface {
	Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error)
}

// Compact 要求策略尝试节省指定 token，并在聚合内采纳新的
// 消息序列。触发判断和最终精确重计数由 application 层完成。
//
// application 在候选 Session 上执行并计数，确认达标后提交 RequestContext。
func (s *Session) Compact(strategy Compactor, minTokenSavings int) (int, error) {
	if strategy == nil {
		return 0, ErrNilCompactor
	}
	msgs, saved, err := strategy.Compress(s.Messages, minTokenSavings)
	if err != nil {
		return 0, err
	}
	s.Messages = sharedkernel.CloneMessages(msgs)
	return saved, nil
}

// ReconcileWindowInput 用 provider 对“下一个完整请求”的精确计数
// 校正窗口账目；随 RequestContext 落盘，下一条 assistant 用实测 usage 覆盖。
func (s *Session) ReconcileWindowInput(inputTokens int) {
	s.WindowToken = sharedkernel.TokenStatistics{TokenInput: inputTokens}
}

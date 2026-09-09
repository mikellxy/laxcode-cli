package reactservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

type ReActService struct {
	Session *session.Session
	// SessRepo 是会话持久化端口：加载与落盘由本服务（application 层）编排，
	// 聚合只做内存内的状态演化，不持有仓储。
	SessRepo            session.SessionRepository
	LLMClient           llmprovider.LLMClient
	ToolRegistry        tools.Registry
	ReActEventConsumerF func(reactEvent *ReactEvent)
	// tracer 是 ReAct/llm-turn span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。类型经 telemetry 别名持有，本包不直接
	// 依赖 OTel（span 的开启与收尾均走 telemetry 辅助函数）。
	tracer telemetry.Tracer
}

var (
	ErrInvalidContextBudget  = errors.New("reactservice: invalid model context budget")
	ErrContextTargetNotReach = errors.New("reactservice: context compaction target cannot be reached")
)

const (
	ReActEventTypeChunk    = "chunk"
	ReActEventTypeToolCall = "tool_call"
	contextTriggerPercent  = 80
	contextTargetPercent   = 60
)

type ReactEvent struct {
	Type       string
	Content    string                    // 工具执行提示
	ChunkEvent *sharedkernel.StreamChunk // LLM 流式增量，仅 chunk 事件携带
}

func NewReActService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer) *ReActService {
	if reActEventConsumerF == nil {
		reActEventConsumerF = func(*ReactEvent) {}
	}
	return &ReActService{
		Session:             sess,
		SessRepo:            sessRepo,
		LLMClient:           llmClient,
		ToolRegistry:        toolRegistry,
		ReActEventConsumerF: reActEventConsumerF,
		tracer:              telemetry.OrNoop(tracer),
	}
}

// InitSession 从仓储读回历史与 token 账目，重建聚合状态（装配 / 续聊期一次）。
func (r *ReActService) InitSession(ctx context.Context) error {
	msgs, err := r.SessRepo.GetMessages(ctx, r.Session.ID)
	if err != nil {
		return err
	}
	r.Session.LoadMessages(msgs)

	meta, err := r.SessRepo.GetMeta(ctx, r.Session.ID)
	if err != nil {
		return err
	}
	r.Session.LoadMeta(meta)

	return nil
}

// InitSysPrompt 写入本次运行的系统提示词：聚合先落定状态并交出消息快照，
// 再由本服务落盘。token 账目不在此写 meta：系统提示词的估算占用每次启动
// 都会重算，真正需要持久化的账目随首条 assistant 消息一起落盘。
func (r *ReActService) InitSysPrompt(ctx context.Context, p string) error {
	sysMsg := r.Session.UpsertSysMessage(p)
	return r.SessRepo.UpsertSysMessage(ctx, r.Session.ID, &sysMsg)
}

// Chat 追加一条用户消息并跑一轮 ReAct 循环，直到模型给出无工具调用的回答。
func (r *ReActService) Chat(ctx context.Context, p string) (*sharedkernel.Message, error) {
	userMsg := r.Session.BuildUserMessage(p)
	if err := r.handleTurnMsg(ctx, &userMsg); err != nil {
		return nil, err
	}
	return r.think(ctx)
}

func (r *ReActService) think(ctx context.Context) (*sharedkernel.Message, error) {
	// session_id 写入 ctx 向下传播：工具注册表的 tool-exec span 经它读取
	// 业务关联键（span 属性不会自动继承）。ReAct span 的父链由调用方 ctx
	// 决定，交互模式下本 span 自动成为 root。
	ctx = telemetry.ContextWithSessionID(ctx, r.Session.ID)
	ctx, reActSpan := telemetry.Start(ctx, r.tracer, telemetry.SpanReAct,
		telemetry.AttrSessionID.String(r.Session.ID),
		telemetry.AttrAgentRole.String(telemetry.AgentRoleMain),
	)
	// run 级 token 合计在 defer 中统一落属性，各 return 路径共享
	var reActInput, reActOutput int
	var reActErr error
	startTime := time.Now()
	defer func() {
		reActSpan.SetAttributes(
			telemetry.AttrInputTokens.Int(reActInput),
			telemetry.AttrOutputTokens.Int(reActOutput),
		)
		telemetry.CloseSpan(reActSpan,
			telemetry.WithErr(reActErr),
			telemetry.WithTimeCostMs(time.Since(startTime).Milliseconds()),
		)
	}()

	turnCnt := 0
	for {
		turnCnt++
		turnCtx, turnSpan := telemetry.Start(ctx, r.tracer, telemetry.LLMTurn,
			telemetry.AttrTurnSeq.Int(turnCnt))
		turnStart := time.Now()
		// closeTurn 是本轮 span 的唯一收尾点：各 return 路径都经它落耗时与错误
		// 状态。span 生命周期留在本函数而不交给持久化辅助函数，本包才能继续
		// 只经 telemetry 使用追踪能力，不直接依赖 OTel 类型。
		closeTurn := func(err error) {
			telemetry.CloseSpan(turnSpan,
				telemetry.WithTimeCostMs(time.Since(turnStart).Milliseconds()),
				telemetry.WithErr(err))
		}

		// 每轮固定一份工具定义：精确计数与随后的生成请求必须
		// 序列化同一份 tools，不能让 registry map 的遍历顺序在两次读取间漂移。
		toolDefs := r.ToolRegistry.GetAvailableTools()
		if err := r.compactContext(turnCtx, toolDefs); err != nil {
			reActErr = err
			closeTurn(err)
			return nil, err
		}

		msg, err := r.LLMClient.GenerateStream(turnCtx, r.Session.Messages, toolDefs, func(chunkEvent sharedkernel.StreamChunk) {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeChunk, ChunkEvent: &chunkEvent})
		})
		if err != nil {
			reActErr = err
			closeTurn(err)
			return nil, err
		}
		if err := r.handleTurnMsg(ctx, msg); err != nil {
			reActErr = err
			closeTurn(err)
			return nil, err
		}
		// llm-turn / ReAct 级 token 用量统计
		reActInput += msg.TokenUsed.TokenInput
		reActOutput += msg.TokenUsed.TokenOutput
		turnSpan.SetAttributes(
			telemetry.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
			telemetry.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
			telemetry.AttrToolCallCount.Int(len(msg.ToolCalls)),
		)

		// 无工具调用，推理循环完成
		if len(msg.ToolCalls) == 0 {
			closeTurn(nil)
			return msg, nil
		}

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			// turnCtx 携带 llm-turn span，tool-exec span 经注册表挂到其下
			result := r.ToolRegistry.Execute(turnCtx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			if err := r.handleTurnMsg(ctx, toolMsg); err != nil {
				reActErr = err
				closeTurn(err)
				return nil, err
			}
		}
		closeTurn(nil)
	}
}

// compactContext 以“下一个完整 provider 请求”为计数口径。占用达到
// 可用输入的 80% 时开始压缩，目标回落到 60%；高低水位避免
// 长会话在每一轮都重复裁剪。每次策略修改后都请 provider 重新计数，
// 未确认达到目标前绝不发送生成请求。
func (r *ReActService) compactContext(ctx context.Context, toolDefs []sharedkernel.ToolDefinition) error {
	budget := r.LLMClient.ContextBudget()
	maxInput := budget.MaxInputTokens()
	if budget.ContextWindow <= 0 || budget.ReservedOutputTokens <= 0 || maxInput <= 0 {
		return fmt.Errorf("%w: context_window=%d reserved_output_tokens=%d",
			ErrInvalidContextBudget, budget.ContextWindow, budget.ReservedOutputTokens)
	}

	current, err := r.LLMClient.CountInputTokens(ctx, r.Session.Messages, toolDefs)
	if err != nil {
		return fmt.Errorf("count context before compaction: %w", err)
	}
	trigger := maxInput * contextTriggerPercent / 100
	target := maxInput * contextTargetPercent / 100
	if current < trigger {
		r.Session.ReconcileWindowInput(current)
		return nil
	}

	for current > target {
		saved, compactErr := r.Session.Compact(compactor.SimpleCompactor, current-target)
		if compactErr != nil {
			return compactErr
		}
		if saved <= 0 {
			return fmt.Errorf("%w: current=%d target=%d", ErrContextTargetNotReach, current, target)
		}

		next, countErr := r.LLMClient.CountInputTokens(ctx, r.Session.Messages, toolDefs)
		if countErr != nil {
			return fmt.Errorf("count context after compaction: %w", countErr)
		}
		if next >= current {
			return fmt.Errorf("%w: provider count made no progress (%d -> %d)",
				ErrContextTargetNotReach, current, next)
		}
		current = next
	}

	r.Session.ReconcileWindowInput(current)
	return nil
}

// handleTurnMsg 把一条消息落盘、同步进聚合，并在 token 账目变化时写回 meta。
// 顺序是“先磁盘后内存”：写盘失败时聚合状态不动，内存与续聊读回的历史
// 不会分叉。本函数只返回 error，span 收尾由调用方的 closeTurn 统一负责。
func (r *ReActService) handleTurnMsg(ctx context.Context, msg *sharedkernel.Message) error {
	if err := r.SessRepo.AppendMessage(ctx, r.Session.ID, msg); err != nil {
		return err
	}
	if err := r.Session.AppendMessage(msg); err != nil {
		return err
	}
	return r.persistMeta(ctx, msg)
}

// persistMeta 在消息改变了 token 账目时把 meta 落盘（meta.json）：只有 assistant
// 消息携带模型返回的实测用量，user / tool 消息不影响账目，无需多写一次文件。
// 落的是本次实测窗口值，不含压缩扣减：压缩只在内存生效，磁盘上的
// history.jsonl 始终是未压缩原文，续聊时按原文重算才自洽。
func (r *ReActService) persistMeta(ctx context.Context, msg *sharedkernel.Message) error {
	if msg.Role != sharedkernel.RoleAssistant {
		return nil
	}
	meta := r.Session.Meta()
	if err := r.SessRepo.UpdateMeta(ctx, r.Session.ID, &meta); err != nil {
		return fmt.Errorf("persist session meta: %w", err)
	}
	return nil
}

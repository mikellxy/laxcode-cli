package reactservice

import (
	"context"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"go.opentelemetry.io/otel/trace"
)

type ReActService struct {
	Session             *session.Session
	SessRepo            session.SessionRepository
	LLMClient           llmprovider.LLMClient
	ToolRegistry        tools.Registry
	ReActEventConsumerF func(reactEvent *ReactEvent)
	// tracer 是 ReAct/llm-turn span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。类型经 telemetry 别名持有，本包不直接
	// 依赖 OTel（span 的开启与收尾均走 telemetry 辅助函数）。
	tracer telemetry.Tracer
}

const (
	ReActEventTypeMsg       = "msg"
	ReActEventTypeReasoning = "reasoning"
	ReActEventTypeToolCall  = "tool_call"
)

// maxWindowToken 是触发上下文压缩的窗口 token 预算，暂写死 200k，
// 未来再做动态配置。
const maxWindowToken = 200_000

type ReactEvent struct {
	Type    string
	Content string
}

func NewReActService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer) *ReActService {
	return &ReActService{
		Session:             sess,
		SessRepo:            sessRepo,
		LLMClient:           llmClient,
		ToolRegistry:        toolRegistry,
		ReActEventConsumerF: reActEventConsumerF,
		tracer:              telemetry.OrNoop(tracer),
	}
}

// InitSession 获取 Session messages、meta
func (r *ReActService) InitSession(ctx context.Context) error {
	msgs, err := r.SessRepo.GetMessages(ctx, r.Session.ID)
	if err != nil {
		return err
	}
	r.Session.LoadMessages(ctx, msgs)

	meta, err := r.SessRepo.GetMeta(ctx, r.Session.ID)
	if err != nil {
		return err
	}
	r.Session.LoadMeta(ctx, meta)

	return nil
}

func (r *ReActService) InitSysPrompt(ctx context.Context, p string) error {
	sysMsg := r.Session.BuildSysMessage(ctx, p)
	if err := r.SessRepo.UpsertSysMessage(ctx, r.Session.ID, sysMsg); err != nil {
		return err
	}
	if err := r.Session.UpsertSysMessage(ctx, sysMsg); err != nil {
		return err
	}
	return nil
}

func (r *ReActService) Chat(ctx context.Context, p string) (*sharedkernel.Message, error) {
	userMsg := r.Session.BuildUserMessage(ctx, p)
	if err := r.SessRepo.AppendMessage(ctx, r.Session.ID, userMsg); err != nil {
		return nil, err
	}
	if err := r.Session.AppendMessage(ctx, userMsg); err != nil {
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

		// 上下文压缩：每轮 generate 前压缩历史（对齐老 engine.Run），触发
		// 阈值 maxWindowToken；压缩后回写 Messages 并同步扣减窗口占用。
		msgs, compressedToken, _ := compactor.SimpleCompactor.Compress(r.Session.Messages, maxWindowToken, r.Session.TokenUsed)
		r.Session.LoadCompactorResult(ctx, msgs, compressedToken)

		msg, err := r.LLMClient.Generate(turnCtx, r.Session.Messages, r.ToolRegistry.GetAvailableTools())
		if err != nil {
			reActErr = err
			telemetry.CloseSpan(turnSpan, telemetry.WithTimeCostMs(time.Since(turnStart).Milliseconds()), telemetry.WithErr(err))
			return nil, err
		}
		err = r.handleTurnMsg(ctx, msg, turnStart, turnSpan)
		if err != nil {
			reActErr = err
			return nil, err
		}
		if msg.ReasoningContent != "" {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeReasoning, Content: msg.ReasoningContent})
		}
		if msg.Content != "" {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeMsg, Content: msg.Content})
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
			telemetry.CloseSpan(turnSpan, telemetry.WithTimeCostMs(time.Since(turnStart).Milliseconds()))
			return msg, nil
		}

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			// turnCtx 携带 llm-turn span，tool-exec span 经注册表挂到其下
			result := r.ToolRegistry.Execute(turnCtx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			err := r.handleTurnMsg(ctx, toolMsg, turnStart, turnSpan)
			if err != nil {
				reActErr = err
				return nil, err
			}
		}
		telemetry.CloseSpan(turnSpan, telemetry.WithTimeCostMs(time.Since(turnStart).Milliseconds()))
	}
}

func (r *ReActService) handleTurnMsg(ctx context.Context, msg *sharedkernel.Message, start time.Time, span trace.Span) error {
	if err := r.SessRepo.AppendMessage(ctx, r.Session.ID, msg); err != nil {
		telemetry.CloseSpan(span, telemetry.WithTimeCostMs(time.Since(start).Milliseconds()), telemetry.WithErr(err))
		return err
	}
	if err := r.Session.AppendMessage(ctx, msg); err != nil {
		telemetry.CloseSpan(span, telemetry.WithTimeCostMs(time.Since(start).Milliseconds()), telemetry.WithErr(err))
		return err
	}
	return nil
}

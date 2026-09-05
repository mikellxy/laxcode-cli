package reactservice

import (
	"context"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/compactor"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"go.opentelemetry.io/otel/trace"
)

type ReActService struct {
	Session             *session.Session
	LLMClient           llmprovider.LLMClient
	ToolRegistry        tools.Registry
	ReActEventConsumerF func(reactEvent *ReactEvent)
	// tracer 是 ReAct/llm-turn span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。
	tracer trace.Tracer
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
	llmClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer trace.Tracer) *ReActService {
	return &ReActService{
		Session:             sess,
		LLMClient:           llmClient,
		ToolRegistry:        toolRegistry,
		ReActEventConsumerF: reActEventConsumerF,
		tracer:              tracing.OrNoop(tracer),
	}
}

func (r *ReActService) Run(ctx context.Context) (*sharedkernel.Message, error) {
	// session_id 写入 ctx 向下传播：工具注册表的 tool-exec span 经它读取
	// 业务关联键（span 属性不会自动继承）。ReAct span 的父链由调用方 ctx
	// 决定，交互模式下本 span 自动成为 root。
	ctx = tracing.ContextWithSessionID(ctx, r.Session.ID)
	ctx, reActSpan := r.tracer.Start(ctx, tracing.SpanReAct,
		trace.WithAttributes(
			tracing.AttrSessionID.String(r.Session.ID),
			tracing.AttrAgentRole.String(tracing.AgentRoleMain),
		))
	// run 级 token 合计在 defer 中统一落属性，各 return 路径共享
	var reActInput, reActOutput int
	var reActErr error
	startTime := time.Now()
	defer func() {
		reActSpan.SetAttributes(
			tracing.AttrInputTokens.Int(reActInput),
			tracing.AttrOutputTokens.Int(reActOutput),
		)
		tracing.CloseSpan(reActSpan,
			tracing.WithErr(reActErr),
			tracing.WithTimeCostMs(time.Since(startTime).Milliseconds()),
		)
	}()

	turnCnt := 0
	for {
		turnCnt++
		turnCtx, turnSpan := r.tracer.Start(ctx, tracing.LLMTurn,
			trace.WithAttributes(tracing.AttrTurnSeq.Int(turnCnt)))
		turnStart := time.Now()

		// 上下文压缩：每轮 generate 前压缩历史（对齐老 engine.Run），触发
		// 阈值 maxWindowToken；压缩后回写 Messages 并同步扣减窗口占用。
		msgs, compressRes := compactor.SimpleCompactor.Compress(r.Session.Messages, maxWindowToken, r.Session.TokenUsed)
		r.Session.Messages = msgs
		if compressRes != nil && compressRes.Total() > 0 {
			r.Session.UpdateWindowToken(ctx, -compressRes.InputTokenCompressed, -compressRes.OutputTokenCompressed)
		}

		msg, err := r.LLMClient.Generate(turnCtx, r.Session.Messages, r.ToolRegistry.GetAvailableTools())
		if err != nil {
			reActErr = err
			tracing.CloseSpan(turnSpan, tracing.WithTimeCostMs(time.Since(turnStart).Milliseconds()), tracing.WithErr(err))
			return nil, err
		}
		if err := r.Session.AppendMessage(ctx, msg); err != nil {
			reActErr = err
			tracing.CloseSpan(turnSpan, tracing.WithTimeCostMs(time.Since(turnStart).Milliseconds()), tracing.WithErr(err))
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
			tracing.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
			tracing.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
			tracing.AttrToolCallCount.Int(len(msg.ToolCalls)),
		)

		// 无工具调用，推理循环完成
		if len(msg.ToolCalls) == 0 {
			tracing.CloseSpan(turnSpan, tracing.WithTimeCostMs(time.Since(turnStart).Milliseconds()))
			return msg, nil
		}

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			// turnCtx 携带 llm-turn span，tool-exec span 经注册表挂到其下
			result := r.ToolRegistry.Execute(turnCtx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			if err := r.Session.AppendMessage(ctx, toolMsg); err != nil {
				reActErr = err
				tracing.CloseSpan(turnSpan, tracing.WithTimeCostMs(time.Since(turnStart).Milliseconds()), tracing.WithErr(err))
				return nil, err
			}
		}
		tracing.CloseSpan(turnSpan, tracing.WithTimeCostMs(time.Since(turnStart).Milliseconds()))
	}
}

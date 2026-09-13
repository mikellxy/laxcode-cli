package reactservice

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	SessRepo  session.SessionRepository
	LLMClient llmprovider.LLMClient
	// ContextSummaryLLMClient 只在确定性本地压缩无法达到目标时调用。
	// 它不参与正常 ReAct 生成，且摘要请求不携带业务工具定义。
	ContextSummaryLLMClient llmprovider.LLMClient
	ToolRegistry            tools.Registry
	Artifacts               tools.ArtifactStore
	ReActEventConsumerF     func(reactEvent *ReactEvent)
	// tracer 是 ReAct/llm-turn span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。类型经 telemetry 别名持有，本包不直接
	// 依赖 OTel（span 的开启与收尾均走 telemetry 辅助函数）。
	tracer telemetry.Tracer
	// agentRole 写入 ReAct span 的 laxcode.agent_role：主服务为 main，
	// 子 Agent 派生服务为 sub（经 NewSubAgentService 构造时注入），
	// 使子会话的 ReAct span 不再被误标为主会话。
	agentRole string
}

var (
	ErrInvalidContextBudget  = errors.New("reactservice: invalid model context budget")
	ErrContextTargetNotReach = errors.New("reactservice: context compaction target cannot be reached")
	ErrPersistRequestContext = errors.New("reactservice: persist request context")
)

const (
	ReActEventTypeChunk      = "chunk"
	ReActEventTypeToolCall   = "tool_call"
	ReActEventTypeRecovery   = "recovery"
	contextTriggerPercent    = 80
	contextTargetPercent     = 60
	recoveryToolResultPrompt = "上一次工具调用未获得可确认的结果；它可能尚未执行，也可能已经执行但结果未被保存。请先检查当前状态，再决定是否重试。"
)

type ReactEvent struct {
	Type       string
	Content    string                    // 工具执行提示
	ChunkEvent *sharedkernel.StreamChunk // LLM 流式增量，仅 chunk 事件携带
}

// RunStats 是一次 Chat 的运行账目：think 循环内已有的轮次 / 工具调用 /
// token 累计统计原本只落 span 属性，此处透出给需要机器判定运行状态的调用方
// （子 Agent 委派边界的完整性判定）。InputTokens/OutputTokens 为实测计费
// 口径，仅累计正常生成轮；FinishReason 为最后一轮的终止原因。
type RunStats struct {
	Turns        int
	ToolCalls    int
	InputTokens  int
	OutputTokens int
	// FinishReason 是最后一个模型轮的终止原因；err 提前返回时为出错轮
	// 已采集的值（可能为空）。
	FinishReason string
}

func NewReActService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	contextSummaryLLMClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer,
	artifactStores ...tools.ArtifactStore) *ReActService {
	if reActEventConsumerF == nil {
		reActEventConsumerF = func(*ReactEvent) {}
	}
	r := &ReActService{
		Session:                 sess,
		SessRepo:                sessRepo,
		LLMClient:               llmClient,
		ContextSummaryLLMClient: contextSummaryLLMClient,
		ToolRegistry:            toolRegistry,
		ReActEventConsumerF:     reActEventConsumerF,
		tracer:                  telemetry.OrNoop(tracer),
		agentRole:               telemetry.AgentRoleMain,
	}
	// ArtifactStore 与数据库会话仓储相互独立；子服务绑定自己的 session ID。
	if len(artifactStores) > 0 && artifactStores[0] != nil {
		store := artifactStores[0]
		r.Artifacts = store
		toolRegistry.Register(tools.NewReadArtifactTool(store, sess.ID))
	}
	return r
}

// NewSubAgentService 构造子 Agent 用的 ReActService：与 NewReActService 的
// 区别仅是 agentRole=sub，使子会话的 ReAct span 角色正确。子 Agent 的完整
// 装配（受限工具集、事件静默）由 SubAgent.Execute 编排。
func NewSubAgentService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	contextSummaryLLMClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer,
	artifactStores ...tools.ArtifactStore) *ReActService {
	r := NewReActService(sess, sessRepo, llmClient, contextSummaryLLMClient,
		toolRegistry, reActEventConsumerF, tracer, artifactStores...)
	r.agentRole = telemetry.AgentRoleSub
	return r
}

// InitSession 从数据库恢复最新工作集。
func (r *ReActService) InitSession(ctx context.Context) error {
	snapshot, err := r.SessRepo.GetRequestContext(ctx, r.Session.ID)
	if err != nil {
		return fmt.Errorf("%w: load request context: %w", ErrPersistRequestContext, err)
	}
	return r.Session.Restore(snapshot)
}

// InitSysPrompt 将本次系统提示词和账目一起提交到工作集快照。
func (r *ReActService) InitSysPrompt(ctx context.Context, p string) error {
	candidate := r.Session.Clone()
	isFirst := len(candidate.Messages) == 0
	sysMsg := candidate.UpsertSysMessage(p)
	if isFirst {
		return r.commitCreatedMessage(ctx, candidate, sysMsg, sysMsg)
	}
	return r.commitUpdatedMessage(ctx, candidate, sysMsg)
}

// Chat 先为数据库中恢复出的未完成 ReAct 补齐缺失的 tool result；随后立即
// 追加本次用户消息，让模型在同一次后续推理中综合旧工具结果与用户的新要求。
func (r *ReActService) Chat(ctx context.Context, p string) (*sharedkernel.Message, error) {
	msg, _, err := r.ChatWithStats(ctx, p)
	return msg, err
}

// ChatWithStats 是 Chat 的带账目变体：需要运行统计（轮次、工具调用、token、
// 终止原因）的调用方使用；前端三个调用方继续走 Chat 保持零改动。
func (r *ReActService) ChatWithStats(ctx context.Context, p string) (*sharedkernel.Message, *RunStats, error) {
	if err := r.recoverBeforeChat(ctx); err != nil {
		return nil, nil, fmt.Errorf("recover previous chat: %w", err)
	}
	userMsg := r.Session.BuildUserMessage(p)
	candidate, err := r.Session.WithAppendedMessage(&userMsg)
	if err != nil {
		return nil, nil, err
	}
	if err := r.commitCreatedMessage(ctx, candidate, userMsg, userMsg); err != nil {
		return nil, nil, err
	}
	return r.think(ctx)
}

// recoverBeforeChat 直接从消息尾部推导上次执行是否收束；若未收束，只补齐
// 最近一次工具调用中未持久化的 tool result，无需额外的活跃对话状态字段。
func (r *ReActService) recoverBeforeChat(ctx context.Context) error {
	if !hasUserMessage(r.Session.Messages) {
		return nil
	}
	tail := r.Session.Messages[len(r.Session.Messages)-1]
	if tail.Role == sharedkernel.RoleAssistant && len(tail.ToolCalls) == 0 {
		return nil
	}
	r.ReActEventConsumerF(&ReactEvent{
		Type:    ReActEventTypeRecovery,
		Content: "检测到上一次对话未完成，正在恢复后继续处理本次输入。",
	})

	for _, call := range missingToolResults(r.Session.Messages) {
		toolMsg := &sharedkernel.Message{
			Role:       sharedkernel.RoleTool,
			ToolCallID: call.ID,
			Content:    recoveryToolResultPrompt,
		}
		if err := r.handleTurnMsg(ctx, toolMsg); err != nil {
			return err
		}
	}
	return nil
}

func hasUserMessage(messages []sharedkernel.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sharedkernel.RoleUser {
			return true
		}
	}
	return false
}

// missingToolResults 只检查最近一个 tool-call assistant。ReAct 在进入下一次
// 模型调用前会持久化该组全部 tool result，因此恢复时最多只有这个调用组未闭合。
func missingToolResults(messages []sharedkernel.Message) []sharedkernel.ToolCall {
	callIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sharedkernel.RoleAssistant && len(messages[i].ToolCalls) > 0 {
			callIndex = i
			break
		}
	}
	if callIndex < 0 {
		return nil
	}

	completed := make(map[string]struct{})
	for i := callIndex + 1; i < len(messages); i++ {
		if messages[i].Role == sharedkernel.RoleTool {
			completed[messages[i].ToolCallID] = struct{}{}
		}
	}
	var missing []sharedkernel.ToolCall
	for _, call := range messages[callIndex].ToolCalls {
		if _, ok := completed[call.ID]; !ok {
			missing = append(missing, call)
		}
	}
	return missing
}

func (r *ReActService) think(ctx context.Context) (*sharedkernel.Message, *RunStats, error) {
	// session_id 写入 ctx 向下传播：工具注册表的 tool-exec span 经它读取
	// 业务关联键（span 属性不会自动继承）。ReAct span 的父链由调用方 ctx
	// 决定，交互模式下本 span 自动成为 root。
	ctx = telemetry.ContextWithSessionID(ctx, r.Session.ID)
	ctx, reActSpan := telemetry.Start(ctx, r.tracer, telemetry.SpanReAct,
		telemetry.AttrSessionID.String(r.Session.ID),
		telemetry.AttrAgentRole.String(r.agentRole),
	)
	// run 级 token 合计在 defer 中统一落属性，各 return 路径共享
	var reActInput, reActOutput int
	var reActErr error
	startTime := time.Now()
	stats := &RunStats{}
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
		stats.Turns = turnCnt
		turnCtx, turnSpan := telemetry.Start(ctx, r.tracer, telemetry.LLMTurn,
			telemetry.AttrTurnSeq.Int(turnCnt),
		)
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
			return nil, stats, err
		}

		msg, err := r.LLMClient.GenerateStream(turnCtx, r.Session.Messages, toolDefs, func(chunkEvent sharedkernel.StreamChunk) {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeChunk, ChunkEvent: &chunkEvent})
		})
		if err != nil {
			// 出错轮已采集的终止原因（provider 在返回错误的同时可能标记
			// cancelled）留给 stats；Think 循环本身不再继续。
			stats.FinishReason = msgFinishReason(msg)
			reActErr = err
			closeTurn(err)
			return msg, stats, err
		}
		if err := r.handleTurnMsg(ctx, msg); err != nil {
			reActErr = err
			closeTurn(err)
			return nil, stats, err
		}
		// llm-turn / ReAct 级 token 用量统计
		reActInput += msg.TokenUsed.TokenInput
		reActOutput += msg.TokenUsed.TokenOutput
		stats.InputTokens += msg.TokenUsed.TokenInput
		stats.OutputTokens += msg.TokenUsed.TokenOutput
		stats.FinishReason = msg.FinishReason
		turnSpan.SetAttributes(
			telemetry.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
			telemetry.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
			telemetry.AttrToolCallCount.Int(len(msg.ToolCalls)),
			telemetry.AttrFinishReason.String(msg.FinishReason),
		)

		// 无工具调用，推理循环完成
		if len(msg.ToolCalls) == 0 {
			closeTurn(nil)
			return msg, stats, nil
		}
		stats.ToolCalls += len(msg.ToolCalls)

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			// turnCtx 携带 llm-turn span，tool-exec span 经注册表挂到其下
			result := r.ToolRegistry.Execute(turnCtx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			if err := r.handleTurnMsg(ctx, toolMsg); err != nil {
				reActErr = err
				closeTurn(err)
				return nil, stats, err
			}
		}
		closeTurn(nil)
	}
}

// msgFinishReason 在错误路径上读取消息可能携带的终止原因；msg 为 nil 或
// 未标记时返回空串。
func msgFinishReason(msg *sharedkernel.Message) string {
	if msg == nil {
		return ""
	}
	return msg.FinishReason
}

// handleTurnMsg 先在候选中赋予稳定标识，再原子提交历史与工作集；成功后内存
// 才切换。JSONL 冷备失败不会使数据库提交失败。
func (r *ReActService) handleTurnMsg(ctx context.Context, msg *sharedkernel.Message) error {
	candidate, err := r.Session.WithAppendedMessage(msg)
	if err != nil {
		return err
	}
	return r.commitCreatedMessage(ctx, candidate, *msg, *msg)
}

func (r *ReActService) commitCreatedMessage(ctx context.Context, candidate *session.Session, original, memory sharedkernel.Message) error {
	revision, err := r.SessRepo.CommitCreateMessage(ctx, r.Session.ID, candidate.RequestContext, original, memory)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

func (r *ReActService) commitUpdatedMessage(ctx context.Context, candidate *session.Session, memory sharedkernel.Message) error {
	revision, err := r.SessRepo.CommitUpdateMessage(ctx, r.Session.ID, candidate.RequestContext, memory)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

func (r *ReActService) commitNextMemoryGeneration(ctx context.Context, candidate *session.Session) error {
	revision, err := r.SessRepo.CommitNextMemoryGeneration(ctx, r.Session.ID, candidate.RequestContext)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

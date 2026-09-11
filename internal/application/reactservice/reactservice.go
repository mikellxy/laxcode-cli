package reactservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Artifacts           tools.ArtifactStore
	ReActEventConsumerF func(reactEvent *ReactEvent)
	// tracer 是 ReAct/llm-turn span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。类型经 telemetry 别名持有，本包不直接
	// 依赖 OTel（span 的开启与收尾均走 telemetry 辅助函数）。
	tracer telemetry.Tracer
}

var (
	ErrInvalidContextBudget   = errors.New("reactservice: invalid model context budget")
	ErrContextTargetNotReach  = errors.New("reactservice: context compaction target cannot be reached")
	ErrPersistRequestContext  = errors.New("reactservice: persist request context")
	ErrInvalidRecoveryContext = errors.New("reactservice: active chat has no recoverable user message")
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

func NewReActService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer,
	artifactStores ...tools.ArtifactStore) *ReActService {
	if reActEventConsumerF == nil {
		reActEventConsumerF = func(*ReactEvent) {}
	}
	r := &ReActService{
		Session:             sess,
		SessRepo:            sessRepo,
		LLMClient:           llmClient,
		ToolRegistry:        toolRegistry,
		ReActEventConsumerF: reActEventConsumerF,
		tracer:              telemetry.OrNoop(tracer),
	}
	// ArtifactStore 与数据库会话仓储相互独立；子服务绑定自己的 session ID。
	if len(artifactStores) > 0 && artifactStores[0] != nil {
		store := artifactStores[0]
		r.Artifacts = store
		toolRegistry.Register(tools.NewReadArtifactTool(store, sess.ID))
	}
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
	candidate.UpsertSysMessage(p)
	return r.commitSnapshot(ctx, candidate)
}

// Chat 先为数据库中恢复出的未完成 ReAct 补齐缺失的 tool result；随后立即
// 追加本次用户消息，让模型在同一次后续推理中综合旧工具结果与用户的新要求。
// 恢复期间 ActiveChatID 保持不变，直到模型生成最终 assistant 才清除。
func (r *ReActService) Chat(ctx context.Context, p string) (*sharedkernel.Message, error) {
	if err := r.recoverBeforeChat(ctx); err != nil {
		return nil, fmt.Errorf("recover previous chat: %w", err)
	}
	userMsg := r.Session.BuildUserMessage(p)
	var (
		candidate *session.Session
		err       error
	)
	if r.Session.ActiveChatID == "" {
		candidate, err = r.Session.WithStartedChat(&userMsg)
	} else {
		// 旧 Chat 尚未收束：新输入作为该执行链的补充/变更要求，
		// 不创建新的 ActiveChatID。
		candidate, err = r.Session.WithAppendedMessage(&userMsg)
	}
	if err != nil {
		return nil, err
	}
	if err := r.commitAppendedMessage(ctx, candidate, userMsg); err != nil {
		return nil, err
	}
	return r.think(ctx)
}

// recoverBeforeChat 检查启动时从数据库恢复出的工作集。ActiveChatID 非空时只
// 补齐未持久化的 tool result；Chat 会紧接着追加用户新输入，再把完整序列
// 一次性交给模型继续推理。
func (r *ReActService) recoverBeforeChat(ctx context.Context) error {
	if r.Session.ActiveChatID == "" {
		return nil
	}

	if !hasUserMessage(r.Session.Messages) {
		return ErrInvalidRecoveryContext
	}
	r.ReActEventConsumerF(&ReactEvent{
		Type:    ReActEventTypeRecovery,
		Content: "检测到上一次对话未完成，正在恢复后继续处理本次输入。",
	})

	// 显式 ActiveChatID 可能来自异常的旧版本快照；若尾部已经是最终回答，
	// 只需持久化清除标记，不能额外再调用一次模型。
	tail := r.Session.Messages[len(r.Session.Messages)-1]
	if tail.Role == sharedkernel.RoleAssistant && len(tail.ToolCalls) == 0 {
		candidate := r.Session.Clone()
		candidate.ActiveChatID = ""
		return r.commitSnapshot(ctx, candidate)
	}

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
	groupID := messages[callIndex].ToolCallGroupID
	for i := callIndex + 1; i < len(messages); i++ {
		if messages[i].Role == sharedkernel.RoleTool &&
			(groupID == "" || messages[i].ToolCallGroupID == groupID) {
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
func (r *ReActService) compactContext(ctx context.Context, toolDefs []sharedkernel.ToolDefinition) (retErr error) {
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

	// 所有修改都在候选工作集上进行；计数失败或未达标时不污染当前上下文。
	startedAt := time.Now()
	beforeInput := current
	beforeStats := collectContextLogStats(r.Session.Messages)
	protectedStart := compactor.ProtectedStart(r.Session.Messages)
	protectedStartSeq := uint64(0)
	if protectedStart < len(r.Session.Messages) {
		protectedStartSeq = r.Session.Messages[protectedStart].Seq
	}
	protectedToolCallGroups := countToolCallGroups(r.Session.Messages[protectedStart:])
	phase := "archive_artifacts"
	passes := 0
	estimatedSaved := 0
	artifactRefsAdded := 0
	afterInput := current

	candidate := r.Session.Clone()
	artifactCandidates := compactor.ArtifactCandidates(candidate.Messages)
	slog.InfoContext(ctx, "context_compaction_triggered",
		"session_id", r.Session.ID,
		"context_window_tokens", budget.ContextWindow,
		"reserved_output_tokens", budget.ReservedOutputTokens,
		"max_input_tokens", maxInput,
		"trigger_tokens", trigger,
		"target_tokens", target,
		"before_input_tokens", beforeInput,
		"before_utilization_ratio", ratio(beforeInput, maxInput),
		"last_seq", r.Session.LastSeq,
		"tool_definition_count", len(toolDefs),
		"message_count", beforeStats.messageCount,
		"assistant_message_count", beforeStats.assistantMessageCount,
		"tool_call_group_count", beforeStats.toolCallGroupCount,
		"tool_call_count", beforeStats.toolCallCount,
		"tool_result_count", beforeStats.toolResultCount,
		"content_bytes", beforeStats.contentBytes,
		"reasoning_bytes", beforeStats.reasoningBytes,
		"existing_artifact_ref_count", beforeStats.artifactRefCount,
		"artifact_candidate_count", len(artifactCandidates),
		"protected_start_index", protectedStart,
		"protected_start_seq", protectedStartSeq,
		"protected_message_count", len(r.Session.Messages)-protectedStart,
		"protected_tool_call_group_count", protectedToolCallGroups,
	)
	defer func() {
		if retErr == nil {
			return
		}
		slog.ErrorContext(ctx, "context_compaction_failed",
			"session_id", r.Session.ID,
			"phase", phase,
			"before_input_tokens", beforeInput,
			"last_counted_input_tokens", afterInput,
			"target_tokens", target,
			"compression_passes", passes,
			"artifact_refs_added", artifactRefsAdded,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", retErr,
		)
	}()

	for _, idx := range artifactCandidates {
		if r.Artifacts == nil {
			return errors.New("artifact store required for tool output compaction")
		}
		ref, err := r.Artifacts.PutArtifact(ctx, r.Session.ID, candidate.Messages[idx].Content)
		if err != nil {
			return fmt.Errorf("archive tool output: %w", err)
		}
		candidate.Messages[idx].Artifact = &ref
		artifactRefsAdded++
	}
	phase = "compress"
	for current > target {
		saved, compactErr := candidate.Compact(compactor.SimpleCompactor, current-target)
		passes++
		if compactErr != nil {
			return compactErr
		}
		if saved <= 0 {
			return fmt.Errorf("%w: current=%d target=%d", ErrContextTargetNotReach, current, target)
		}

		next, countErr := r.LLMClient.CountInputTokens(ctx, candidate.Messages, toolDefs)
		if countErr != nil {
			return fmt.Errorf("count context after compaction: %w", countErr)
		}
		if next >= current {
			return fmt.Errorf("%w: provider count made no progress (%d -> %d)",
				ErrContextTargetNotReach, current, next)
		}
		estimatedSaved += saved
		current = next
		afterInput = next
	}

	candidate.ReconcileWindowInput(current)
	phase = "snapshot"
	if err := r.commitSnapshot(ctx, candidate); err != nil {
		return err
	}
	phase = "completed"
	afterStats := collectContextLogStats(candidate.Messages)
	slog.InfoContext(ctx, "context_compaction_completed",
		"session_id", r.Session.ID,
		"before_input_tokens", beforeInput,
		"after_input_tokens", current,
		"exact_saved_tokens", beforeInput-current,
		"estimated_saved_tokens", estimatedSaved,
		"input_reduction_ratio", ratio(beforeInput-current, beforeInput),
		"after_utilization_ratio", ratio(current, maxInput),
		"target_tokens", target,
		"target_met", current <= target,
		"compression_passes", passes,
		"artifact_refs_added", artifactRefsAdded,
		"message_count_before", beforeStats.messageCount,
		"message_count_after", afterStats.messageCount,
		"content_bytes_before", beforeStats.contentBytes,
		"content_bytes_after", afterStats.contentBytes,
		"reasoning_bytes_before", beforeStats.reasoningBytes,
		"reasoning_bytes_after", afterStats.reasoningBytes,
		"artifact_ref_count_after", afterStats.artifactRefCount,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	return nil
}

type contextLogStats struct {
	messageCount          int
	assistantMessageCount int
	toolCallGroupCount    int
	toolCallCount         int
	toolResultCount       int
	contentBytes          int
	reasoningBytes        int
	artifactRefCount      int
}

func collectContextLogStats(messages []sharedkernel.Message) contextLogStats {
	stats := contextLogStats{messageCount: len(messages)}
	for _, message := range messages {
		stats.contentBytes += len(message.Content)
		stats.reasoningBytes += len(message.ReasoningContent)
		if message.Artifact != nil {
			stats.artifactRefCount++
		}
		switch message.Role {
		case sharedkernel.RoleAssistant:
			stats.assistantMessageCount++
			if len(message.ToolCalls) > 0 {
				stats.toolCallGroupCount++
				stats.toolCallCount += len(message.ToolCalls)
			}
		case sharedkernel.RoleTool:
			stats.toolResultCount++
		}
	}
	return stats
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func countToolCallGroups(messages []sharedkernel.Message) int {
	count := 0
	for _, message := range messages {
		if message.Role == sharedkernel.RoleAssistant && len(message.ToolCalls) > 0 {
			count++
		}
	}
	return count
}

// handleTurnMsg 先在候选中赋予稳定标识，再原子提交历史与工作集；成功后内存
// 才切换。JSONL 冷备失败不会使数据库提交失败。
func (r *ReActService) handleTurnMsg(ctx context.Context, msg *sharedkernel.Message) error {
	candidate, err := r.Session.WithAppendedMessage(msg)
	if err != nil {
		return err
	}
	return r.commitAppendedMessage(ctx, candidate, *msg)
}

func (r *ReActService) commitSnapshot(ctx context.Context, candidate *session.Session) error {
	// 同步提交只读借用候选工作集，无需再次深复制；仓储返回后才切换内存。
	revision, err := r.SessRepo.CommitSnapshot(ctx, r.Session.ID, candidate.RequestContext)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

func (r *ReActService) commitAppendedMessage(
	ctx context.Context,
	candidate *session.Session,
	newMsg sharedkernel.Message,
) error {
	// newMsg 显式说明本次新增的 original；仓储会校验它与候选尾消息等价，
	// 并校验 ActiveChatID 双向约束：最终 assistant 必须已清空、其余消息须
	// 处于活跃对话。违反时提交被整体拒绝，候选须由调用方保证满足契约。
	revision, err := r.SessRepo.CommitAppendedMessage(
		ctx, r.Session.ID, candidate.RequestContext, newMsg,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

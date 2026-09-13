package reactservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// compactionRun 承载本地压缩与 LLM 摘要两阶段共享的可变状态：候选工作集、
// 最新精确计数，以及 failed/completed 两条日志都要读的观测字段。
type compactionRun struct {
	toolDefs            []sharedkernel.ToolDefinition
	candidate           *session.Session
	protectedStart      int
	target              int
	// current 是最近一次 provider 精确计数的输入 token 数；两阶段每使
	// 计数下降都要回写，编排层据此决定是否进入下一阶段。
	current             int
	summarySource       []sharedkernel.Message
	phase               string
	passes              int
	estimatedSaved      int
	artifactRefsAdded   int
	summaryCalls        int
	summaryInputTokens  int
	summaryOutputTokens int
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

	run := &compactionRun{
		toolDefs:       toolDefs,
		candidate:      r.Session.Clone(),
		protectedStart: protectedStart,
		target:         target,
		current:        current,
	}
	artifactCandidates := compactor.ArtifactCandidates(run.candidate.Messages)
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
			"phase", run.phase,
			"before_input_tokens", beforeInput,
			"last_counted_input_tokens", run.current,
			"target_tokens", target,
			"compression_passes", run.passes,
			"artifact_refs_added", run.artifactRefsAdded,
			"summary_calls", run.summaryCalls,
			"summary_input_tokens", run.summaryInputTokens,
			"summary_output_tokens", run.summaryOutputTokens,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", retErr,
		)
	}()

	if err := r.compactLocally(ctx, run, artifactCandidates); err != nil {
		return err
	}
	// 本地优先、LLM 摘要兜底：确定性裁剪达不到目标时才让摘要模型出场。
	if run.current > run.target {
		if err := r.compactWithSummary(ctx, run); err != nil {
			return err
		}
	}

	run.candidate.ReconcileWindowInput(run.current)
	run.phase = "advance_generation"
	if err := run.candidate.AdvanceMemoryGeneration(); err != nil {
		return err
	}
	run.phase = "snapshot"
	if err := r.commitNextMemoryGeneration(ctx, run.candidate); err != nil {
		return err
	}
	run.phase = "completed"
	afterStats := collectContextLogStats(run.candidate.Messages)
	slog.InfoContext(ctx, "context_compaction_completed",
		"session_id", r.Session.ID,
		"before_input_tokens", beforeInput,
		"after_input_tokens", run.current,
		"exact_saved_tokens", beforeInput-run.current,
		"estimated_saved_tokens", run.estimatedSaved,
		"input_reduction_ratio", ratio(beforeInput-run.current, beforeInput),
		"after_utilization_ratio", ratio(run.current, maxInput),
		"target_tokens", target,
		"target_met", run.current <= target,
		"compression_passes", run.passes,
		"artifact_refs_added", run.artifactRefsAdded,
		"summary_calls", run.summaryCalls,
		"summary_input_tokens", run.summaryInputTokens,
		"summary_output_tokens", run.summaryOutputTokens,
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

// compactLocally 归档大体积工具输出并做确定性裁剪。summarySource 必须在
// 归档之后（携带刚写入的 artifact 引用）、本地裁剪之前（保留尚未被裁剪
// 的信息）快照，供 compactWithSummary 使用。
func (r *ReActService) compactLocally(ctx context.Context, run *compactionRun, artifactCandidates []int) error {
	for _, idx := range artifactCandidates {
		if r.Artifacts == nil {
			return errors.New("artifact store required for tool output compaction")
		}
		ref, err := r.Artifacts.PutArtifact(ctx, r.Session.ID, run.candidate.Messages[idx].Content)
		if err != nil {
			return fmt.Errorf("archive tool output: %w", err)
		}
		run.candidate.Messages[idx].Artifact = &ref
		run.artifactRefsAdded++
	}
	if run.protectedStart > 1 {
		run.summarySource = sharedkernel.CloneMessages(run.candidate.Messages[1:run.protectedStart])
	}
	run.phase = "compress"
	for run.current > run.target {
		saved, compactErr := run.candidate.Compact(compactor.SimpleCompactor, run.current-run.target)
		run.passes++
		if compactErr != nil {
			return compactErr
		}
		if saved <= 0 {
			break
		}

		next, countErr := r.LLMClient.CountInputTokens(ctx, run.candidate.Messages, run.toolDefs)
		if countErr != nil {
			return fmt.Errorf("count context after compaction: %w", countErr)
		}
		if next >= run.current {
			break
		}
		run.estimatedSaved += saved
		run.current = next
	}
	return nil
}

// compactWithSummary 用专用摘要模型压缩保护区之前的旧历史，并合并进候选
// 工作集。模型可能不严格遵守 token 上限，只允许一次基于已有摘要的再压缩，
// 避免故障模型造成无界调用和费用。
func (r *ReActService) compactWithSummary(ctx context.Context, run *compactionRun) error {
	run.phase = "summarize"
	if r.ContextSummaryLLMClient == nil || len(run.summarySource) == 0 {
		return fmt.Errorf("%w: current=%d target=%d summarizable_messages=%d summary_client_configured=%t",
			ErrContextTargetNotReach, run.current, run.target, len(run.summarySource), r.ContextSummaryLLMClient != nil)
	}

	firstSeq := run.summarySource[0].Seq
	lastSeq := run.summarySource[len(run.summarySource)-1].Seq
	summaryPrefix := fmt.Sprintf("以下是原始消息 seq %d-%d 的结构化历史摘要，不是新的用户请求：\n", firstSeq, lastSeq)
	baseMessages, mergeErr := compactor.MergeSummary(run.candidate.Messages, run.protectedStart, summaryPrefix)
	if mergeErr != nil {
		return mergeErr
	}
	baseInput, countErr := r.LLMClient.CountInputTokens(ctx, baseMessages, run.toolDefs)
	if countErr != nil {
		return fmt.Errorf("count context summary base: %w", countErr)
	}
	maxSummaryTokens := run.target - baseInput
	if maxSummaryTokens <= 0 {
		return fmt.Errorf("%w: protected context input=%d target=%d", ErrContextTargetNotReach, baseInput, run.target)
	}

	normalized, usage, summaryErr := r.generateContextSummary(ctx, run.summarySource, "", maxSummaryTokens)
	run.summaryCalls++
	run.summaryInputTokens += usage.TokenInput
	run.summaryOutputTokens += usage.TokenOutput
	if summaryErr != nil {
		return summaryErr
	}
	merged, mergeErr := compactor.MergeSummary(run.candidate.Messages, run.protectedStart, summaryPrefix+normalized)
	if mergeErr != nil {
		return mergeErr
	}
	next, countErr := r.LLMClient.CountInputTokens(ctx, merged, run.toolDefs)
	if countErr != nil {
		return fmt.Errorf("count context after llm summary: %w", countErr)
	}

	if next > run.target {
		stricterTarget := maxSummaryTokens - (next - run.target)
		if stricterTarget <= 0 {
			return fmt.Errorf("%w: summarized input=%d target=%d", ErrContextTargetNotReach, next, run.target)
		}
		normalized, usage, summaryErr = r.generateContextSummary(ctx, nil, normalized, stricterTarget)
		run.summaryCalls++
		run.summaryInputTokens += usage.TokenInput
		run.summaryOutputTokens += usage.TokenOutput
		if summaryErr != nil {
			return summaryErr
		}
		merged, mergeErr = compactor.MergeSummary(run.candidate.Messages, run.protectedStart, summaryPrefix+normalized)
		if mergeErr != nil {
			return mergeErr
		}
		next, countErr = r.LLMClient.CountInputTokens(ctx, merged, run.toolDefs)
		if countErr != nil {
			return fmt.Errorf("count context after llm summary retry: %w", countErr)
		}
	}
	if next > run.target {
		return fmt.Errorf("%w: summarized input=%d target=%d", ErrContextTargetNotReach, next, run.target)
	}
	run.candidate.Messages = merged
	run.candidate.TokenUsed.Add(sharedkernel.TokenStatistics{
		TokenInput: run.summaryInputTokens, TokenOutput: run.summaryOutputTokens,
	})
	run.current = next
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

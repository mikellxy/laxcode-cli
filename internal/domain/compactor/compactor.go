// Package compactor 提供 agent 运行时上下文的纯内存压缩策略。
// 触发时机、provider 精确 token 计数和模型窗口预算由 application 层
// 编排；本包只负责在给定的最小节省目标下选择要裁剪的内容。
package compactor

import (
	"fmt"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	oldToolOutputTokenThreshold = 50
	recentContentRuneLimit      = 1000
)

// simpleStrategy 是默认的确定性轻压缩策略，无状态且可并发使用。
type simpleStrategy struct{}

// SimpleCompactor 是默认压缩策略。
var SimpleCompactor simpleStrategy

type toolSpan struct {
	assistantIdx int
	resultIdxs   []int
}

// Compress 至少尝试节省 minTokenSavings 个 token。它返回独立的消息
// 切片，不修改调用方的原始历史。saved 是本地估算值，只用于决定
// 下一个裁剪动作；最终是否达标必须由 provider 重新精确计数确认。
//
// 裁剪顺序：旧工具 span 的输出 → 旧 reasoning → 旧 assistant 正文
// → 最新工具 span 的超长输出。工具调用消息和所有对应结果消息
// 始终保留，从而不破坏 function_call/function_call_output 配对。
func (simpleStrategy) Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error) {
	const reActToolCallTurnKept = 3
	if minTokenSavings < 0 {
		return nil, 0, fmt.Errorf("compactor: min token savings must not be negative")
	}
	out := cloneMessages(msgs)
	if minTokenSavings == 0 || len(out) == 0 {
		return out, 0, nil
	}

	spans, orphanResults := collectToolSpans(out)
	saved := 0
	reached := func() bool { return saved >= minTokenSavings }

	// 以 span 为原子单位处理：同一 assistant turn 发出的并行工具
	// 结果一起保留或一起清理，不会出现“只留最后一个结果”。
	for i := 0; i+reActToolCallTurnKept < len(spans); i++ {
		for _, idx := range spans[i].resultIdxs {
			if sharedkernel.EstimateTokenInt(out[idx].Content) <= oldToolOutputTokenThreshold {
				continue
			}
			placeholder := clearedToolOutput(out[idx])
			saved += replaceContent(&out[idx], placeholder)
		}
		if reached() {
			return out, saved, nil
		}
	}

	// 没有可识别 function_call 的孤立 tool 消息也使用正确 RoleTool
	// 处理；常规生产轨迹不会走到这个兼容分支。
	for _, idx := range orphanResults {
		if sharedkernel.EstimateTokenInt(out[idx].Content) <= oldToolOutputTokenThreshold {
			continue
		}
		saved += replaceContent(&out[idx], clearedToolOutput(out[idx]))
		if reached() {
			return out, saved, nil
		}
	}

	// 从最新往回定位第 reActToolCallTurnKept+1 个“带工具调用的 assistant”，
	// 它及其之前都属于旧轮次；下标更大的才是需要完整保留的最近区间。
	// 工具调用轮次不足时保持 -1，表示最近区间覆盖到开头、无需裁剪。
	lastToolCallAssistantIdx := -1
	assistantToolCallSeen := 0
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == sharedkernel.RoleAssistant && len(out[i].ToolCalls) > 0 {
			assistantToolCallSeen++
			if assistantToolCallSeen > reActToolCallTurnKept {
				lastToolCallAssistantIdx = i
				break
			}
		}
	}

	// 回收 reActTurnKept 之外的 reasoning_content
	for i := range out {
		if out[i].Role != sharedkernel.RoleAssistant || i > lastToolCallAssistantIdx || out[i].ReasoningContent == "" {
			continue
		}
		old := out[i].ReasoningContent
		out[i].ReasoningContent = ""
		saved += sharedkernel.EstimateTokenInt(old)
		if reached() {
			return out, saved, nil
		}
	}

	// 剪裁 reActTurnKept 之外的 assistant 消息的 content
	for i := range out {
		if out[i].Role != sharedkernel.RoleAssistant || i > lastToolCallAssistantIdx {
			continue
		}
		if truncated, ok := truncateMiddleRunes(out[i].Content, recentContentRuneLimit); ok {
			saved += replaceContent(&out[i], truncated)
			if reached() {
				return out, saved, nil
			}
		}
	}

	// 最近 reActToolCallTurnKept 个 span 是当前 ReAct 轮次马上要消费的结果：
	// 保留全部 call/result 结构，仅对每个超长结果做 UTF-8 安全的头尾截断。
	// 用 len(spans) 而非 len(out) 索引，并夹住下界，避免越界与重复处理旧 span。
	for i := len(spans) - 1; i >= 0 && i >= len(spans)-reActToolCallTurnKept; i-- {
		span := spans[i]
		for _, idx := range span.resultIdxs {
			if truncated, ok := truncateMiddleRunes(out[idx].Content, recentContentRuneLimit); ok {
				saved += replaceContent(&out[idx], truncated)
			}
		}
	}

	return out, saved, nil
}

func collectToolSpans(msgs []sharedkernel.Message) ([]toolSpan, []int) {
	var spans []toolSpan
	callToSpan := make(map[string]int)
	for i, msg := range msgs {
		if msg.Role != sharedkernel.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		spanIdx := len(spans)
		spans = append(spans, toolSpan{assistantIdx: i})
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				callToSpan[call.ID] = spanIdx
			}
		}
	}

	var orphans []int
	for i, msg := range msgs {
		if msg.Role != sharedkernel.RoleTool {
			continue
		}
		spanIdx, ok := callToSpan[msg.ToolCallID]
		if !ok {
			orphans = append(orphans, i)
			continue
		}
		spans[spanIdx].resultIdxs = append(spans[spanIdx].resultIdxs, i)
	}
	return spans, orphans
}

func cloneMessages(msgs []sharedkernel.Message) []sharedkernel.Message {
	out := make([]sharedkernel.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if msgs[i].ToolCalls != nil {
			out[i].ToolCalls = append([]sharedkernel.ToolCall(nil), msgs[i].ToolCalls...)
		}
	}
	return out
}

func clearedToolOutput(msg sharedkernel.Message) string {
	return fmt.Sprintf("[早期工具输出已清理 call_id=%s，原始长度=%d字符]",
		msg.ToolCallID, utf8.RuneCountInString(msg.Content))
}

func replaceContent(msg *sharedkernel.Message, content string) int {
	before := sharedkernel.EstimateTokenInt(msg.Content)
	after := sharedkernel.EstimateTokenInt(content)
	if after >= before {
		return 0
	}
	msg.Content = content
	return before - after
}

// truncateMiddleRunes 只在内容超过 limit 时截断，并且始终在 rune
// 边界切分，避免中文、emoji 等多字节字符变成非法 UTF-8。
func truncateMiddleRunes(content string, limit int) (string, bool) {
	runes := []rune(content)
	if limit <= 0 || len(runes) <= limit {
		return content, false
	}
	headLen := limit / 2
	tailLen := limit - headLen
	removed := len(runes) - limit
	return fmt.Sprintf("%s [...中间%d字符已截断...] %s",
		string(runes[:headLen]), removed, string(runes[len(runes)-tailLen:])), true
}

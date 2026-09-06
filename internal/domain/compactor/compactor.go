// Package compactor 提供 agent 运行时上下文的压缩能力：在每轮 LLM 生成前
// 按窗口 token 预算裁剪历史消息（清理早期工具输出、截断超长正文、丢弃陈旧
// reasoning），以控制发给模型的上下文规模。
//
// 本包属领域层：“上下文窗口紧张时该保留什么、丢弃什么”是 agent 的业务策略，
// 全部计算都在内存中完成（只用 fmt 与 unicode），不涉任何 I/O、OS 机制或
// 第三方 SDK，故不是基础设施。仅依赖 domain/sharedkernel。
package compactor

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// CompressResult 记录一次压缩节省的 token 量（输入侧 / 输出侧分列）。
type CompressResult struct {
	InputTokenCompressed  int
	OutputTokenCompressed int
}

// Total 返回本次压缩节省的总 token 量。
func (c *CompressResult) Total() int {
	return c.OutputTokenCompressed + c.InputTokenCompressed
}

// Strategy 是上下文压缩策略抽象。
type Strategy interface {
	Compress(msgs []sharedkernel.Message, maxToken int, winConsumed sharedkernel.TokenStatistics) ([]sharedkernel.Message, *CompressResult)
}

// simpleStrategy 是默认的简单压缩策略：无状态，可安全并发使用。
type simpleStrategy struct {
	// inMemoryMsgsCnt 是“完整保留、不做任何裁剪”的尾部消息条数。
	// 取 1 即本策略的当前行为：只有最后一条消息被视为在内存中。
	inMemoryMsgsCnt int
}

// SimpleCompactor 是默认的简单压缩策略实现。
var SimpleCompactor simpleStrategy = simpleStrategy{inMemoryMsgsCnt: 1}

// 编译期确保 simpleStrategy 满足 Strategy 接口。
var _ Strategy = SimpleCompactor

// Compress 在窗口占用达到 maxToken 的 80% 时裁剪 msgs：仅最后
// s.inMemoryMsgsCnt 条消息视为"在内存中"完整保留，其余的工具输出 / 超长正文
// 按规则清理或截断，早于最后一次用户输入的 reasoning 直接丢弃。
// 未达阈值时原样返回、result 为零值。传入的 msgs 会被原地修改并返回。
func (s simpleStrategy) Compress(msgs []sharedkernel.Message, maxToken int, winConsumed sharedkernel.TokenStatistics) ([]sharedkernel.Message, *CompressResult) {
	result := new(CompressResult)
	if float64(winConsumed.TokenInput+winConsumed.TokenOutput) < float64(maxToken)*0.8 {
		return msgs, result
	}

	minInMemoryIdx := len(msgs) - s.inMemoryMsgsCnt

	// Reasoning only matters since the last human input; older ones are
	// stale and get dropped to save context tokens.
	lastUserIdx := -1
	for i, m := range msgs {
		if m.Role == sharedkernel.RoleUser && m.ToolCallID == "" {
			lastUserIdx = i
		}
	}

	for i, msg := range msgs {
		inMemory := i >= minInMemoryIdx

		newContent := msg.Content
		if msg.Role == sharedkernel.RoleUser && msg.ToolCallID != "" {
			if !inMemory {
				if len(msg.Content) > 200 {
					newContent = fmt.Sprintf("为节省上下文空间，早起工具输出已被系统清理。原始输出长度为:%d字节", len(msg.Content))
				}
				result.InputTokenCompressed += EstimateTokenInt(msg.Content) - EstimateTokenInt(newContent)
			} else if len(msg.Content) > 1000 {
				head := msg.Content[:500]
				tail := msg.Content[len(msg.Content)-500:]
				newContent = fmt.Sprintf("%s [...输出过长，中间%d字节已被截断...] %s", head, len(msg.Content)-1000, tail)
				result.InputTokenCompressed += EstimateTokenInt(msg.Content) - EstimateTokenInt(newContent)
			}
		}

		if msg.Role == sharedkernel.RoleAssistant && msg.Content != "" {
			if !inMemory && len(msg.Content) > 1000 {
				head := msg.Content[:500]
				tail := msg.Content[len(msg.Content)-500:]
				newContent = fmt.Sprintf("%s [...早起推理输出过长，中间%d字节已被截断...] %s", head, len(msg.Content)-1000, tail)
				result.OutputTokenCompressed += EstimateTokenInt(msg.Content) - EstimateTokenInt(newContent)
			}
		}

		// Write back through the slice index: msg is a per-iteration copy.
		msgs[i].Content = newContent

		if msg.Role == sharedkernel.RoleAssistant && i < lastUserIdx && msg.ReasoningContent != "" {
			result.OutputTokenCompressed += EstimateTokenInt(msg.ReasoningContent)
			msgs[i].ReasoningContent = ""
		}
	}

	return msgs, result
}

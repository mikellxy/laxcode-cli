package context

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/schema"
)

type CompressResult struct {
	InputTokenCompressed  int
	OutputTokenCompressed int
}

func (c *CompressResult) Total() int {
	return c.OutputTokenCompressed + c.InputTokenCompressed
}

type Strategy interface {
	Compress(msgs []schema.Message, maxToken int) (schema.Message, *CompressResult)
}

type simpleStrategy struct {
	inMemoryMsgsCnt int
}

var SimpleCompactor simpleStrategy = simpleStrategy{inMemoryMsgsCnt: 8}

func (s simpleStrategy) Compress(msgs []schema.Message, maxToken int, winConsumed schema.TokenStatistics) ([]schema.Message, *CompressResult) {
	result := new(CompressResult)
	if float64(winConsumed.Total()) < float64(maxToken)*0.8 {
		return msgs, result
	}

	minInMemoryIdx := len(msgs) - 1

	// Reasoning only matters since the last human input; older ones are
	// stale and get dropped to save context tokens.
	lastUserIdx := -1
	for i, m := range msgs {
		if m.Role == schema.RoleUser && m.ToolCallID == "" {
			lastUserIdx = i
		}
	}

	for i, msg := range msgs {
		inMemory := i >= minInMemoryIdx

		newContent := msg.Content
		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
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

		if msg.Role == schema.RoleAssistant && msg.Content != "" {
			if !inMemory && len(msg.Content) > 1000 {
				head := msg.Content[:500]
				tail := msg.Content[len(msg.Content)-500:]
				newContent = fmt.Sprintf("%s [...早起推理输出过长，中间%d字节已被截断...] %s", head, len(msg.Content)-1000, tail)
				result.OutputTokenCompressed += EstimateTokenInt(msg.Content) - EstimateTokenInt(newContent)
			}
		}

		// Write back through the slice index: msg is a per-iteration copy.
		msgs[i].Content = newContent

		if msg.Role == schema.RoleAssistant && i < lastUserIdx && msg.ReasoningContent != "" {
			result.OutputTokenCompressed += EstimateTokenInt(msg.ReasoningContent)
			msgs[i].ReasoningContent = ""
		}
	}

	return msgs, result
}

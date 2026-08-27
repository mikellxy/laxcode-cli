package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

// executeToolCalls 执行一轮工具调用，返回与入参顺序一一对应的结果切片。
// 并行策略：调用数 >1 且全部为 read_file 时，用 goroutine + channel
// fork-join 并发读取（只读、互不依赖，天然可并行）；其余情况退化为顺序执行。
func (f *AgentEngine) executeToolCalls(ctx context.Context, calls []schema.ToolCall) []*schema.ToolResult {
	if len(calls) <= 1 || !tools.AllReadFile(calls) {
		results := make([]*schema.ToolResult, 0, len(calls))
		for i := range calls {
			results = append(results, f.ToolRegistry.Execute(ctx, &calls[i]))
		}
		return results
	}

	// fork：每个 read_file 调用一个 goroutine，带序号的执行结果经
	// 有缓冲 channel 回传，缓冲容量等于调用数，保证发送永不阻塞。
	type indexedResult struct {
		idx    int
		result *schema.ToolResult
	}
	ch := make(chan indexedResult, len(calls))
	for i := range calls {
		go func(idx int, call *schema.ToolCall) {
			ch <- indexedResult{idx: idx, result: f.ToolRegistry.Execute(ctx, call)}
		}(i, &calls[i])
	}

	// join：收齐全部结果后按原始调用顺序归位，确保 tool_call_id 与消息一一对应。
	results := make([]*schema.ToolResult, len(calls))
	for range calls {
		r := <-ch
		results[r.idx] = r.result
	}
	return results
}

// buildToolResultContent 把单次工具执行结果拼成回写给模型的文本：
// 成功直接返回输出；失败则在错误信息后附上错误指引提示词与原始输出，
// 引导模型按 suggestion 修正后重试。
func buildToolResultContent(name string, toolResult *schema.ToolResult) string {
	content := toolResult.Output
	if toolResult.Error == nil {
		return content
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("error executing tool %s: %s", name, toolResult.Error))
	// 错误携带指引提示词时附到工具返回末尾，引导模型按 suggestion 修正
	var promptErr laxctx.ErrorWithPrompt
	if errors.As(toolResult.Error, &promptErr) {
		if prompt, ok := promptErr.AsPrompt(); ok {
			sb.WriteString("\n")
			sb.WriteString(prompt)
		}
	}
	// 工具报错时若仍有输出（如 shell 的 stderr/stdout），一并附上供模型定位问题
	if len(toolResult.Output) > 0 {
		sb.WriteString("\n以下为工具执行时的原始输出，供定位错误参考:\n")
		sb.WriteString(toolResult.Output)
	}
	return sb.String()
}

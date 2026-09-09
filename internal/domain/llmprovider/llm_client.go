package llmprovider

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type LLMClient interface {
	Generate(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition) (*sharedkernel.Message, error)
	// GenerateStream 通过 emit 推送增量事件，并返回完整的生成消息。
	GenerateStream(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition, emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error)
	// CountInputTokens 按与 Generate 相同的序列化口径计算待发送请求的
	// 输入 token，包括消息、reasoning/function-call items 和工具定义。
	CountInputTokens(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition) (int, error)
	// ContextBudget 返回当前模型/部署的窗口预算；由 provider 持有，
	// 避免 application 层对具体模型名称做硬编码推断。
	ContextBudget() ContextBudget
}

// ContextBudget defines the request window. ReservedOutputTokens is also sent
// to the provider as max_output_tokens, making the maximum input deterministic.
type ContextBudget struct {
	ContextWindow        int
	ReservedOutputTokens int
}

// MaxInputTokens 返回在预留输出后可用的最大输入 token。
func (b ContextBudget) MaxInputTokens() int {
	return b.ContextWindow - b.ReservedOutputTokens
}

package llmprovider

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type LLMClient interface {
	Generate(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition) (*sharedkernel.Message, error)
	// GenerateStream 通过 emit 推送增量事件，并返回完整的生成消息。
	GenerateStream(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition, emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error)
}

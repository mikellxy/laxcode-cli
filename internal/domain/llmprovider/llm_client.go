package llmprovider

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type LLMClient interface {
	Generate(ctx context.Context, msgs []sharedkernel.Message, tools []sharedkernel.ToolDefinition) (*sharedkernel.Message, error)
}

package tools

import (
	"context"

	"github.com/mikellxy/laxcode/internal/schema"
)

type Registry interface {
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, toolCall *schema.ToolCall) *schema.ToolResult
}

type Tool struct {
	Name       string
	Definition schema.ToolDefinition
	ExecFunc   func(ctx context.Context, toolCall *schema.ToolCall) (string, error)
}

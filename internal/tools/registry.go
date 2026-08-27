package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mikellxy/laxcode/internal/schema"
)

type Registry interface {
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, toolCall *schema.ToolCall) *schema.ToolResult
	Register(tool BaseTool)
}

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	BeforeExecInfo(json.RawMessage) string
	AfterExecInfo(json.RawMessage) string
}

type DefaultRegistry struct {
	db map[string]BaseTool
}

func NewDefaultRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		db: make(map[string]BaseTool),
	}
}

func (d *DefaultRegistry) GetAvailableTools() []schema.ToolDefinition {
	var toolDefs []schema.ToolDefinition
	for _, tool := range d.db {
		toolDefs = append(toolDefs, tool.Definition())
	}
	return toolDefs
}

func (d *DefaultRegistry) Execute(ctx context.Context, toolCall *schema.ToolCall) *schema.ToolResult {
	tool, ok := d.db[toolCall.Name]
	if !ok {
		return &schema.ToolResult{
			Error:      errors.New("tool not found"),
			Output:     fmt.Sprintf("tool %s not exists", toolCall.Name),
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	fmt.Printf("\033[33m[LaxCode] tool execute... %s\033[0m\n", tool.BeforeExecInfo(toolCall.Arguments))

	output, err := tool.Execute(ctx, toolCall.Arguments)
	if err != nil {
		return &schema.ToolResult{
			Error:      err,
			Output:     output,
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	return &schema.ToolResult{
		Output:     output,
		IsError:    false,
		ToolCallID: toolCall.ID,
	}
}

func (d *DefaultRegistry) Register(tool BaseTool) {
	d.db[tool.Name()] = tool
}

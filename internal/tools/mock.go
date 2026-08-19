package tools

import (
	"context"

	"github.com/mikellxy/laxcode/internal/schema"
)

type fakeRegistry struct {
	db map[string]*Tool
}

func NewFakeRegistry() *fakeRegistry {
	registry := &fakeRegistry{
		db: make(map[string]*Tool),
	}

	registry.db["get_weather"] = &Tool{
		Name: "get_weather",
		Definition: schema.ToolDefinition{
			Name:        "get_weather",
			Description: "Get weather of a location, the user should supply a location first.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{
						"type":        "string",
						"description": "The city and state, e.g. San Francisco, CA",
					},
				},
				"required": []string{"location"},
			},
		},
		ExecFunc: func(ctx context.Context, toolCall *schema.ToolCall) (string, error) {
			return "38℃", nil
		},
	}

	return registry
}

func (f fakeRegistry) GetAvailableTools() []schema.ToolDefinition {
	var tools []schema.ToolDefinition
	for _, tool := range f.db {
		tools = append(tools, tool.Definition)
	}
	return tools
}

func (f fakeRegistry) Execute(ctx context.Context, toolCall *schema.ToolCall) *schema.ToolResult {
	tool := f.db[toolCall.Name]
	output, err := tool.ExecFunc(ctx, toolCall)
	return &schema.ToolResult{
		ToolCallID: toolCall.ID,
		Output:     output,
		IsError:    err != nil,
	}
}

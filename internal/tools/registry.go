package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mikellxy/laxcode/internal/printer"
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
	// printer 输出工具执行前提示；工具调用提示的宿主是 registry
	// （BeforeExecInfo 是 tool 的方法、tool 查找在此进行，engine 拿不到
	// tool 实例），故经构造注入输出实例。nil 时取包级默认实例。
	printer printer.Printer
}

func NewDefaultRegistry(p printer.Printer) *DefaultRegistry {
	if p == nil {
		p = printer.Default()
	}
	return &DefaultRegistry{
		db:      make(map[string]BaseTool),
		printer: p,
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

	d.printer.PrintToolCall(tool.BeforeExecInfo(toolCall.Arguments))

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

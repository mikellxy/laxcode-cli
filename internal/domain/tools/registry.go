package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
)

type Closer interface {
	Close() error
}

type Registry interface {
	GetAvailableTools() []sharedkernel.ToolDefinition
	Execute(ctx context.Context, toolCall *sharedkernel.ToolCall) *sharedkernel.ToolResult
	Register(tool BaseTool)
	BeforeExecInfo(toolCall *sharedkernel.ToolCall) string
}

type BaseTool interface {
	Name() string
	Definition() sharedkernel.ToolDefinition
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

func (d *DefaultRegistry) GetAvailableTools() []sharedkernel.ToolDefinition {
	var toolDefs []sharedkernel.ToolDefinition
	for _, tool := range d.db {
		toolDefs = append(toolDefs, tool.Definition())
	}
	return toolDefs
}

func (d *DefaultRegistry) BeforeExecInfo(toolCall *sharedkernel.ToolCall) string {
	tool, ok := d.db[toolCall.Name]
	if !ok {
		return ""
	}
	return tool.BeforeExecInfo(toolCall.Arguments)
}

func (d *DefaultRegistry) Execute(ctx context.Context, toolCall *sharedkernel.ToolCall) *sharedkernel.ToolResult {
	var execErr error

	attrs := []attribute.KeyValue{tracing.AttrToolName.String(toolCall.Name)}
	if sid := tracing.SessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, tracing.AttrSessionID.String(sid))
	}

	tool, ok := d.db[toolCall.Name]
	if !ok {
		execErr = errors.New("tool not found")
		return &sharedkernel.ToolResult{
			Error:      execErr,
			Output:     fmt.Sprintf("tool %s not exists", toolCall.Name),
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	output, execErr := tool.Execute(ctx, toolCall.Arguments)
	if execErr != nil {
		return &sharedkernel.ToolResult{
			Error:      execErr,
			Output:     output,
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	return &sharedkernel.ToolResult{
		Output:     output,
		IsError:    false,
		ToolCallID: toolCall.ID,
	}
}

func (d *DefaultRegistry) Register(tool BaseTool) {
	d.db[tool.Name()] = tool
}

// Close 关闭实现了 closer 的已注册工具（如 bash 工具回收后台进程与
// 临时文件），由前端 loop 在运行结束时调用；未实现者跳过
func (d *DefaultRegistry) Close() error {
	var errs []error
	for _, tool := range d.db {
		if c, ok := tool.(Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func ToolResultAsMsg(ctx context.Context, toolName string, result *sharedkernel.ToolResult) *sharedkernel.Message {
	msg := &sharedkernel.Message{
		Role:       sharedkernel.RoleTool,
		ToolCallID: result.ToolCallID,
	}
	if result.Error == nil {
		msg.Content = result.Output
		return msg
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("error executing tool %s: %s", toolName, result.Error))
	// 错误携带指引提示词时附到工具返回末尾，引导模型按 suggestion 修正
	var promptErr laxctx.ErrorWithPrompt
	if errors.As(result.Error, &promptErr) {
		if prompt, ok := promptErr.AsPrompt(); ok {
			sb.WriteString("\n")
			sb.WriteString(prompt)
		}
	}
	// 工具报错时若仍有输出（如 shell 的 stderr/stdout），一并附上供模型定位问题
	if len(result.Output) > 0 {
		sb.WriteString("\n以下为工具执行时的原始输出，供定位错误参考:\n")
		sb.WriteString(result.Output)
	}
	msg.Content = sb.String()

	return msg
}

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	// tracer 是工具执行 span 的追踪注入点，经构造注入；nil 缺省 noop，
	// 不产生任何观测输出。
	tracer trace.Tracer
}

func NewDefaultRegistry(tracer trace.Tracer) *DefaultRegistry {
	return &DefaultRegistry{
		db:     make(map[string]BaseTool),
		tracer: tracing.OrNoop(tracer),
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
	timeStart := time.Now()
	var execErr error

	attrs := []attribute.KeyValue{tracing.AttrToolName.String(toolCall.Name)}
	if sid := tracing.SessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, tracing.AttrSessionID.String(sid))
	}
	ctx, span := d.tracer.Start(ctx, tracing.SpanToolExec, trace.WithAttributes(attrs...))
	defer func() {
		tracing.CloseSpan(span,
			tracing.WithTimeCostMs(time.Since(timeStart).Milliseconds()),
			tracing.WithErr(execErr),
		)
	}()

	tool, ok := d.db[toolCall.Name]
	if !ok {
		execErr = errors.New("tool not found")
		raw := fmt.Sprintf("tool %s not exists", toolCall.Name)
		return &sharedkernel.ToolResult{
			Error:      execErr,
			Output:     buildToolResultContent(toolCall.Name, execErr, raw),
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	output, execErr := tool.Execute(ctx, toolCall.Arguments)
	if execErr != nil {
		// 与老 engine.buildToolResultContent 对齐：执行失败时在返回前就把
		// 自愈引导提示词与原始输出包进 Output，使 ToolResult.Output 成为
		// 可直接回写模型的最终内容
		return &sharedkernel.ToolResult{
			Error:      execErr,
			Output:     buildToolResultContent(toolCall.Name, execErr, output),
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

// buildToolResultContent 把单次工具执行结果拼成回写给模型的文本：成功直接
// 返回输出；失败则在错误信息后附上自愈引导提示词与原始输出，引导模型按
// suggestion 修正后重试。逻辑与老 engine.buildToolResultContent 对齐，供
// Execute 在返回 ToolResult 前包装 Output 使用。
func buildToolResultContent(name string, execErr error, output string) string {
	if execErr == nil {
		return output
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("error executing tool %s: %s", name, execErr))
	// 错误携带指引提示词时附到工具返回末尾，引导模型按 suggestion 修正
	var promptErr ErrorWithPrompt
	if errors.As(execErr, &promptErr) {
		if prompt, ok := promptErr.AsPrompt(); ok {
			sb.WriteString("\n")
			sb.WriteString(prompt)
		}
	}
	// 工具报错时若仍有输出（如 shell 的 stderr/stdout），一并附上供模型定位问题
	if len(output) > 0 {
		sb.WriteString("\n以下为工具执行时的原始输出，供定位错误参考:\n")
		sb.WriteString(output)
	}
	return sb.String()
}

// ToolResultAsMsg 把工具执行结果套上 tool 消息外壳（Role/ToolCallID）。
// 自愈引导提示词已在 Registry.Execute 阶段包进 result.Output，此处直接
// 透传 Output 作为消息正文，不再重复包装。
func ToolResultAsMsg(result *sharedkernel.ToolResult) *sharedkernel.Message {
	return &sharedkernel.Message{
		Role:       sharedkernel.RoleTool,
		ToolCallID: result.ToolCallID,
		Content:    result.Output,
	}
}

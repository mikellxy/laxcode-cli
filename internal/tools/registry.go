package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	// tracer 是工具执行 span 的追踪注入点（同 printer 一样经构造注入，
	// 宿主理由相同）。nil 时缺省 noop，不产生任何观测输出。
	tracer trace.Tracer
}

func NewDefaultRegistry(p printer.Printer, t trace.Tracer) *DefaultRegistry {
	if p == nil {
		p = printer.Default()
	}
	return &DefaultRegistry{
		db:      make(map[string]BaseTool),
		printer: p,
		tracer:  tracing.OrNoop(t),
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
	// tool-exec span：记录工具名与耗时，不记 token。session_id 经 ctx
	// 传播而来（Run 在循环开始时写入）；span 父子关系同样经 ctx——
	// 并行 read_file 分支的 goroutine 沿用同一 ctx，自然成为兄弟 span。
	attrs := []attribute.KeyValue{tracing.AttrToolName.String(toolCall.Name)}
	if sid := tracing.SessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, tracing.AttrSessionID.String(sid))
	}
	ctx, span := d.tracer.Start(ctx, tracing.SpanToolExec, trace.WithAttributes(attrs...))
	defer span.End()

	tool, ok := d.db[toolCall.Name]
	if !ok {
		err := errors.New("tool not found")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return &schema.ToolResult{
			Error:      err,
			Output:     fmt.Sprintf("tool %s not exists", toolCall.Name),
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	d.printer.PrintToolCall(tool.BeforeExecInfo(toolCall.Arguments))

	output, err := tool.Execute(ctx, toolCall.Arguments)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
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

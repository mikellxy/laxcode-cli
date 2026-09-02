package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Closer interface {
	Close() error
}

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
	timeStart := time.Now()
	var execErr error

	attrs := []attribute.KeyValue{tracing.AttrToolName.String(toolCall.Name)}
	if sid := tracing.SessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, tracing.AttrSessionID.String(sid))
	}
	ctx, span := d.tracer.Start(ctx, tracing.SpanToolExec, trace.WithAttributes(attrs...))
	defer func() {
		tracing.CloseSpan(span,
			tracing.WithTimeCostMs(time.Since(timeStart).Microseconds()),
			tracing.WithErr(execErr),
		)
	}()

	tool, ok := d.db[toolCall.Name]
	if !ok {
		execErr = errors.New("tool not found")
		return &schema.ToolResult{
			Error:      execErr,
			Output:     fmt.Sprintf("tool %s not exists", toolCall.Name),
			IsError:    true,
			ToolCallID: toolCall.ID,
		}
	}

	d.printer.PrintToolCall(tool.BeforeExecInfo(toolCall.Arguments))

	output, execErr := tool.Execute(ctx, toolCall.Arguments)
	if execErr != nil {
		return &schema.ToolResult{
			Error:      execErr,
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

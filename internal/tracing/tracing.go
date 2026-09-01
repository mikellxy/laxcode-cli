package tracing

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// instrumentationName 是 laxcode 全部 span 的 instrumentation scope 名
const instrumentationName = "github.com/mikellxy/laxcode"

// Handle 是 tracing 的装配句柄：持有供引擎与工具注册表注入的 Tracer，
// 以及进程退出前须调用的 Shutdown 钩子。main 创建并持有它，两个前端
// 退出路径各 defer 一次 Shutdown。
type Handle struct {
	Tracer   trace.Tracer
	provider trace.TracerProvider
}

// New 以 TracerProvider 构造句柄；nil provider 缺省为 OTel 官方 noop
// 实现——此时全部埋点零开销、不产生任何观测输出。
func New(tp trace.TracerProvider) *Handle {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return &Handle{Tracer: tp.Tracer(instrumentationName), provider: tp}
}

// Shutdown 在进程退出前调用：实现侧（如官方 SDK 的 TracerProvider）
// 具备 Shutdown(context.Context) error 方法时转发，以强制 flush 尚未
// 导出的 span（批量导出间隔可能长于 one-shot 进程存活时间）；否则为空
// 操作。
func (h *Handle) Shutdown(ctx context.Context) error {
	if s, ok := h.provider.(interface{ Shutdown(context.Context) error }); ok {
		return s.Shutdown(ctx)
	}
	return nil
}

// OrNoop 把 nil tracer 归一为 noop：引擎与注册表的构造注入点允许调用方
// 传 nil 表示不启用追踪。
func OrNoop(t trace.Tracer) trace.Tracer {
	if t == nil {
		return noop.NewTracerProvider().Tracer(instrumentationName)
	}
	return t
}

// sessionIDKey 是 session_id 在 context 中传播的私有键：Registry 等不
// 持有 session 引用的埋点经它读取业务关联键。span 属性不会自动继承，
// 故以 ctx value 显式传播。
type sessionIDKey struct{}

// ContextWithSessionID 把 session_id 写入 ctx，供下游埋点读取。
// Agent 在每次 Run 开始时调用；子 Agent 以子 session id 覆盖，
// 使嵌套子树中的工具 span 归属各自会话。
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext 读取 ctx 中携带的 session_id；未携带返回空串。
func SessionIDFromContext(ctx context.Context) string {
	sid, _ := ctx.Value(sessionIDKey{}).(string)
	return sid
}

func CloseSpan(span trace.Span, opts ...opt) {
	o := new(options)
	for _, opt := range opts {
		opt(o)
	}
	if o.timeCostMs > 0 {
		span.SetAttributes(AttrTimeCostMs.Int64(o.timeCostMs))
	}
	if o.err != nil {
		span.SetStatus(codes.Error, o.err.Error())
		span.RecordError(o.err)
	}
	span.End()
}

type options struct {
	timeCostMs int64
	err        error
}

type opt func(o *options)

func WithTimeCostMs(costMs int64) opt {
	return func(o *options) {
		o.timeCostMs = costMs
	}
}

func WithErr(err error) opt {
	return func(o *options) {
		o.err = err
	}
}

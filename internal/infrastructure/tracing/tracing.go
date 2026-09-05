// Package tracing 是 DDD 架构下的 OpenTelemetry 埋点语义约定与装配入口，
// 由老 internal/tracing 复制而来，供 domain/application 层使用而不依赖老的
// 非 DDD 代码。集中定义 span 名与属性键常量、提供 Tracer 派生与 noop 缺省、
// session_id 的 ctx 传播、span 关闭辅助，以及进程退出前的 Shutdown 钩子。
//
// 产品只依赖 OTel API 模块，不提供任何真实上报后端的实现。用户接入方式：
// 在 infrastructure/tracing/custom 下自行实现 trace.TracerProvider（其
// Span.End 即上报触发点）并在 init 中经 Register 注入 HandleDB；主程序
// 启动时遍历 HandleDB 选用，或在使用方进程装配官方 SDK 后把
// otel.GetTracerProvider() 传入 New。批量与导出策略由实现方决定。
//
// 注意：OTel API 的 TracerProvider / Tracer / Span 均为密封接口（内嵌未导出
// 方法），自定义实现须按官方 "API Implementations" 约定内嵌
// go.opentelemetry.io/otel/trace/embedded 包中的对应接口来满足。
package tracing

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// instrumentationName 是 laxcode 全部 span 的 instrumentation scope 名
const instrumentationName = "github.com/mikellxy/laxcode"

// Handle 是 tracing 的装配句柄：持有供应用服务与工具注册表注入的 Tracer，
// 以及进程退出前须调用的 Shutdown 钩子。装配方创建并持有它，退出路径
// defer 一次 Shutdown。
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
// 导出的 span（批量导出间隔可能长于进程存活时间）；否则为空操作。
func (h *Handle) Shutdown(ctx context.Context) error {
	if s, ok := h.provider.(interface{ Shutdown(context.Context) error }); ok {
		return s.Shutdown(ctx)
	}
	return nil
}

// OrNoop 把 nil tracer 归一为 noop：应用服务与注册表的构造注入点允许
// 调用方传 nil 表示不启用追踪。
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
// ReActService 在每次 Run 开始时调用，使嵌套子树中的工具 span 归属会话。
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext 读取 ctx 中携带的 session_id；未携带返回空串。
func SessionIDFromContext(ctx context.Context) string {
	sid, _ := ctx.Value(sessionIDKey{}).(string)
	return sid
}

// CloseSpan 统一 span 收尾：按需落耗时属性、记录错误状态，最后 End。
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

// WithTimeCostMs 为 CloseSpan 附带耗时（毫秒）属性。
func WithTimeCostMs(costMs int64) opt {
	return func(o *options) {
		o.timeCostMs = costMs
	}
}

// WithErr 为 CloseSpan 附带错误：非 nil 时置 span 状态为 Error 并记录。
func WithErr(err error) opt {
	return func(o *options) {
		o.err = err
	}
}

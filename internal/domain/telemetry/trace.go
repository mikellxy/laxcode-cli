// telemetry 内的无副作用埋点辅助：不持有任何 TracerProvider/上报后端，
// 仅依赖 OTel API 完成 tracer 归一、session_id 的 ctx 传播与 span 收尾。
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// InstrumentationName 是 laxcode 全部 span 的 instrumentation scope 名。
// 领域埋点（本包的 noop 归一）与基础设施装配（infrastructure/tracing 的
// Tracer 派生）共用，确保落入同一 scope。
const InstrumentationName = "github.com/mikellxy/laxcode"

// OrNoop 把 nil tracer 归一为 noop：应用服务与注册表的构造注入点允许
// 调用方传 nil 表示不启用追踪。
func OrNoop(t trace.Tracer) trace.Tracer {
	if t == nil {
		return noop.NewTracerProvider().Tracer(InstrumentationName)
	}
	return t
}

// sessionIDKey 是 session_id 在 context 中传播的私有键：tools.Registry 等
// 不持有 session 引用的埋点经它读取业务关联键。span 属性不会自动继承，
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

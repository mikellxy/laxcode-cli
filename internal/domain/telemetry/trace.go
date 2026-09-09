// 本文件是 telemetry 内的无副作用埋点辅助：不持有任何 TracerProvider/上报
// 后端，仅依赖 OTel API 完成类型别名、tracer 归一、session_id 的 ctx 传播、
// span 开启与收尾。
//
// 注：本注释与 package 子句之间刻意空一行，避免与 attrs.go 的包文档
// 构成重复的 package doc。

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// InstrumentationName 是 laxcode 全部 span 的 instrumentation scope 名。
// 领域埋点（本包的 noop 归一）与基础设施装配（infrastructure/tracing 的
// Tracer 派生）共用，确保落入同一 scope。
const InstrumentationName = "github.com/mikellxy/laxcode"

// 本包对 OTel API 类型的别名：埋点方（domain/tools 注册表、application/
// reactservice）一律经这些名字使用追踪能力，不再直接 import
// go.opentelemetry.io/ 下的任何包，将来换观测方案时改动收敛在本包与
// infrastructure/tracing 两处。
//
// 刻意用类型别名（=）而非自定义接口：OTel API 本身就是厂商中立的稳定
// 抽象，再包一层等价接口只是重复劳动，还会让 infrastructure/tracing 与
// filetrace 的适配代码平白多一层转换。
type (
	// Tracer 是追踪注入点。构造注入时允许传 nil，由 OrNoop 归一。
	Tracer = trace.Tracer
	// Span 是进行中的 span 句柄，收尾统一走 CloseSpan。
	Span = trace.Span
	// KeyValue 是一条 span 属性；键取自本包的 Attr* 常量。
	KeyValue = attribute.KeyValue
)

// NoopTracer 返回不产生任何观测输出的 tracer：显式关闭追踪的装配与测试
// 用它替代 nil，以覆盖「非 nil tracer」的代码路径。
func NoopTracer() Tracer {
	return noop.NewTracerProvider().Tracer(InstrumentationName)
}

// OrNoop 把 nil tracer 归一为 noop：应用服务与注册表的构造注入点允许
// 调用方传 nil 表示不启用追踪。
func OrNoop(t Tracer) Tracer {
	if t == nil {
		return NoopTracer()
	}
	return t
}

// Start 开启一个 span 并把它写入返回的 ctx：spanName 取自本包的 Span* /
// LLMTurn 常量，attrs 为该 span 的业务属性。父子链完全由 ctx 决定，调用方
// 无需接触 OTel 的 SpanStartOption。tracer 为 nil 时自动退化为 noop，
// 因而本函数可安全用于未装配追踪的路径。
func Start(ctx context.Context, tracer Tracer, spanName string, attrs ...KeyValue) (context.Context, Span) {
	return OrNoop(tracer).Start(ctx, spanName, trace.WithAttributes(attrs...))
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
func CloseSpan(span Span, opts ...opt) {
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

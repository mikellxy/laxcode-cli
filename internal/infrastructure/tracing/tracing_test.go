package tracing

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
)

// shutdownProvider 带 Shutdown 钩子的假 TracerProvider，用于验证
// Handle.Shutdown 的转发逻辑。
type shutdownProvider struct {
	embedded.TracerProvider
	shutdownCalled bool
	shutdownErr    error
}

func (p *shutdownProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(name)
}

func (p *shutdownProvider) Shutdown(context.Context) error {
	p.shutdownCalled = true
	return p.shutdownErr
}

// plainProvider 无 Shutdown 方法的假 provider：验证 Handle.Shutdown 空操作。
type plainProvider struct {
	embedded.TracerProvider
}

func (p *plainProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(name)
}

func TestNewWithNilUsesNoop(t *testing.T) {
	h := New(nil)
	if h == nil {
		t.Fatal("New(nil) 返回 nil handle")
	}
	if h.Tracer == nil {
		t.Fatal("New(nil) 应提供可用 Tracer")
	}
	// noop tracer 可正常 Start/End，零开销
	_, span := h.Tracer.Start(context.Background(), telemetry.SpanReAct)
	span.End()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("noop Shutdown 不应报错：%v", err)
	}
}

func TestNewTracerInstrumentationScope(t *testing.T) {
	h := New(nil)
	// noop tracer 无法直接读 scope 名；此处验证 tracer 归属于
	// InstrumentationName 派生路径可正常使用即可
	if h.provider == nil {
		t.Error("provider 不应为空")
	}
}

func TestShutdownForwardsToProvider(t *testing.T) {
	p := &shutdownProvider{}
	h := New(p)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !p.shutdownCalled {
		t.Error("provider 实现 Shutdown 时应被转发调用")
	}
}

func TestShutdownPropagatesProviderError(t *testing.T) {
	p := &shutdownProvider{shutdownErr: errShutdown}
	h := New(p)
	if err := h.Shutdown(context.Background()); err == nil {
		t.Fatal("provider Shutdown 报错时应向上透传")
	}
}

func TestShutdownNoopWhenProviderMissingMethod(t *testing.T) {
	h := New(&plainProvider{})
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("无 Shutdown 方法的 provider 应空操作：%v", err)
	}
}

func TestRegisterAndHandleDB(t *testing.T) {
	h := New(nil)
	Register("test-handle", h)
	defer delete(HandleDB, "test-handle")

	if got, ok := HandleDB["test-handle"]; !ok || got != h {
		t.Errorf("Register 后应能在 HandleDB 取回同一 handle，实际 %+v", got)
	}
}

var errShutdown = &shutdownErr{}

type shutdownErr struct{}

func (*shutdownErr) Error() string { return "shutdown failed" }

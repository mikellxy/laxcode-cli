package tracing

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

func TestNewNilProviderGivesNoop(t *testing.T) {
	t.Parallel()
	h := New(nil)
	if h.Tracer == nil {
		t.Fatal("New(nil) 应给出可用的 noop Tracer")
	}
	// noop span：可正常 Start/End 且不记录（IsRecording=false）
	ctx, span := h.Tracer.Start(context.Background(), ReactLoop)
	span.SetAttributes(AttrSessionID.String("s-1"))
	span.End()
	if span.IsRecording() {
		t.Error("noop span 不应处于 recording 状态")
	}
	if SessionIDFromContext(ctx) != "" {
		t.Error("Start 不应隐式写入 session_id")
	}
}

func TestShutdownWithoutMethodIsNoop(t *testing.T) {
	t.Parallel()
	// noop provider 无 Shutdown 方法：接口断言不命中，空操作
	if err := New(nil).Shutdown(context.Background()); err != nil {
		t.Fatalf("noop provider 的 Shutdown 应为空操作，实际返回 %v", err)
	}
}

// shutdownStubProvider 携带 Shutdown 方法的 TracerProvider 测试桩。
// 嵌入 embedded.TracerProvider 是 OTel 官方要求的接口实现姿势
// （接口含私有标记方法，嵌入后由嵌入值满足）。
type shutdownStubProvider struct {
	embedded.TracerProvider
	called int
	err    error
}

func (p *shutdownStubProvider) Tracer(string, ...trace.TracerOption) trace.Tracer {
	return New(nil).Tracer
}

func (p *shutdownStubProvider) Shutdown(context.Context) error {
	p.called++
	return p.err
}

func TestShutdownForwarding(t *testing.T) {
	t.Parallel()
	p := &shutdownStubProvider{}
	if err := New(p).Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 应透传 nil 错误，实际 %v", err)
	}
	if p.called != 1 {
		t.Fatalf("Shutdown 应转发一次，实际 %d 次", p.called)
	}

	want := errors.New("flush failed")
	p.err = want
	if err := New(p).Shutdown(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Shutdown 应透传实现侧错误，实际 %v", err)
	}
}

func TestOrNoop(t *testing.T) {
	t.Parallel()
	if OrNoop(nil) == nil {
		t.Fatal("OrNoop(nil) 应返回 noop Tracer")
	}
	custom := New(&shutdownStubProvider{}).Tracer
	if OrNoop(custom) != custom {
		t.Error("OrNoop 应原样返回非 nil Tracer")
	}
}

func TestSessionIDContextRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("空 ctx 应读不到 session_id，实际 %q", got)
	}
	ctx = ContextWithSessionID(ctx, "s-1")
	if got := SessionIDFromContext(ctx); got != "s-1" {
		t.Errorf("应读到写入的 session_id，实际 %q", got)
	}
	// 子 Agent 场景：后写覆盖先写
	ctx = ContextWithSessionID(ctx, "s-2")
	if got := SessionIDFromContext(ctx); got != "s-2" {
		t.Errorf("覆盖后应读到新值，实际 %q", got)
	}
}

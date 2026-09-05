package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// recordingSpan 记录 CloseSpan 对该 span 的操作，供断言。
type recordingSpan struct {
	embedded.Span
	ended      bool
	statusCode codes.Code
	statusDesc string
	recorded   []error
	attrs      []attribute.KeyValue
}

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.attrs = append(s.attrs, kv...)
}

func (s *recordingSpan) AddLink(trace.Link)                    {}
func (s *recordingSpan) AddEvent(string, ...trace.EventOption) {}
func (s *recordingSpan) IsRecording() bool                     { return false }
func (s *recordingSpan) SpanContext() trace.SpanContext        { return trace.SpanContext{} }
func (s *recordingSpan) SetName(string)                        {}
func (s *recordingSpan) TracerProvider() trace.TracerProvider  { return nil }

func (s *recordingSpan) SetStatus(code codes.Code, description string) {
	s.statusCode = code
	s.statusDesc = description
}

func (s *recordingSpan) RecordError(err error, _ ...trace.EventOption) {
	s.recorded = append(s.recorded, err)
}

func (s *recordingSpan) End(_ ...trace.SpanEndOption) {
	s.ended = true
}

// recordingTracer 记录由它 Start 出来的 span。
type recordingTracer struct {
	embedded.Tracer
	spans []*recordingSpan
}

func (t *recordingTracer) Start(_ context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	s := new(recordingSpan)
	t.spans = append(t.spans, s)
	return context.Background(), s
}

func TestOrNoop(t *testing.T) {
	if got := OrNoop(nil); got == nil {
		t.Fatal("OrNoop(nil) 不应返回 nil")
	}
	// nil 归一后仍可正常 Start，不 panic
	ctx, span := OrNoop(nil).Start(context.Background(), SpanReAct)
	if span == nil {
		t.Fatal("noop tracer Start 不应返回 nil span")
	}
	if ctx == nil {
		t.Fatal("noop tracer Start 不应返回 nil ctx")
	}
	span.End()

	fake := &recordingTracer{}
	if got := OrNoop(fake); got != trace.Tracer(fake) {
		t.Error("OrNoop(非 nil) 应原样返回传入 tracer")
	}
}

func TestSessionIDContextPropagation(t *testing.T) {
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("未携带 session_id 时应返回空串，实际 %q", got)
	}

	ctx = ContextWithSessionID(ctx, "sess-42")
	if got := SessionIDFromContext(ctx); got != "sess-42" {
		t.Errorf("应取回 sess-42，实际 %q", got)
	}
}

func TestCloseSpanWithoutOpts(t *testing.T) {
	s := new(recordingSpan)
	CloseSpan(s)
	if !s.ended {
		t.Error("CloseSpan 应调用 span.End")
	}
	if s.statusCode != codes.Unset {
		t.Errorf("无错误时不应置状态，实际 %v", s.statusCode)
	}
	if len(s.recorded) != 0 {
		t.Errorf("无错误时不应 RecordError，实际 %v", s.recorded)
	}
	if len(s.attrs) != 0 {
		t.Errorf("无耗时参数时不应落属性，实际 %v", s.attrs)
	}
}

func TestCloseSpanWithError(t *testing.T) {
	s := new(recordingSpan)
	sentinel := errors.New("boom")
	CloseSpan(s, WithErr(sentinel))
	if !s.ended {
		t.Error("应调用 span.End")
	}
	if s.statusCode != codes.Error || s.statusDesc != "boom" {
		t.Errorf("错误时应置 Error 状态并写描述，实际 %v/%q", s.statusCode, s.statusDesc)
	}
	if len(s.recorded) != 1 || s.recorded[0] != sentinel {
		t.Errorf("应记录传入的错误，实际 %v", s.recorded)
	}
}

func TestCloseSpanWithTimeCost(t *testing.T) {
	s := new(recordingSpan)
	CloseSpan(s, WithTimeCostMs(123))
	if !s.ended {
		t.Error("应调用 span.End")
	}
	found := false
	for _, kv := range s.attrs {
		if kv.Key == AttrTimeCostMs && kv.Value.AsInt64() == 123 {
			found = true
		}
	}
	if !found {
		t.Errorf("应写入 time_cost_ms=123 属性，实际 %v", s.attrs)
	}
}

func TestCloseSpanWithErrorAndTimeCost(t *testing.T) {
	s := new(recordingSpan)
	sentinel := errors.New("oops")
	CloseSpan(s, WithErr(sentinel), WithTimeCostMs(7))
	if s.statusCode != codes.Error {
		t.Errorf("应置 Error 状态，实际 %v", s.statusCode)
	}
	found := false
	for _, kv := range s.attrs {
		if kv.Key == AttrTimeCostMs && kv.Value.AsInt64() == 7 {
			found = true
		}
	}
	if !found {
		t.Errorf("应同时写入耗时属性，实际 %v", s.attrs)
	}
}

func TestStartWithRecordingTracer(t *testing.T) {
	tr := &recordingTracer{}
	_, span := tr.Start(context.Background(), SpanReAct)
	if _, ok := span.(*recordingSpan); !ok {
		t.Fatalf("recordingTracer.Start 应返回 recordingSpan，实际 %T", span)
	}
	if len(tr.spans) != 1 {
		t.Fatalf("应记录 1 个 span，实际 %d", len(tr.spans))
	}
}

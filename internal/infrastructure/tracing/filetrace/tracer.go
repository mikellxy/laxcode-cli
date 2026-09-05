package filetrace

import (
	"context"
	"crypto/rand"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// tracer 是 filetrace 的 Tracer 实现，负责生成 span 并维护 trace/span ID。
type tracer struct {
	embedded.Tracer

	provider  *Provider
	scopeName string
}

// Start 创建一个新 span。若 ctx 中已有合法 span，则继承其 trace_id 并记录
// parent_span_id；否则生成一条新 trace。
func (t *tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	start := time.Now().UTC()
	if !cfg.Timestamp().IsZero() {
		start = cfg.Timestamp()
	}

	s := &span{
		tracer:    t,
		name:      name,
		attrs:     attrsToMap(cfg.Attributes()),
		startTime: start,
		events:    make([]eventRecord, 0),
	}

	parent := trace.SpanFromContext(ctx)
	if parent.SpanContext().IsValid() {
		s.traceID = parent.SpanContext().TraceID()
		s.parentSpanID = parent.SpanContext().SpanID()
	} else {
		s.traceID = randomTraceID()
	}
	s.spanID = randomSpanID()
	s.spanCtx = trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: s.traceID,
		SpanID:  s.spanID,
	})

	return trace.ContextWithSpan(ctx, s), s
}

// randomTraceID 生成 16 字节随机 trace ID。
func randomTraceID() trace.TraceID {
	var id trace.TraceID
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		panic(err)
	}
	return id
}

// randomSpanID 生成 8 字节随机 span ID。
func randomSpanID() trace.SpanID {
	var id trace.SpanID
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		panic(err)
	}
	return id
}

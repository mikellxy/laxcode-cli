package filetrace

import (
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// span 是文件日志型 Tracer 的 Span 实现。它在内存中累积属性、事件、状态，
// 在 End 时序列化为一条 JSON Line 写入文件。
type span struct {
	embedded.Span

	mu           sync.Mutex
	tracer       *tracer
	name         string
	attrs        map[string]any
	statusCode   codes.Code
	statusMsg    string
	events       []eventRecord
	startTime    time.Time
	endTime      time.Time
	ended        bool
	traceID      trace.TraceID
	spanID       trace.SpanID
	parentSpanID trace.SpanID
	spanCtx      trace.SpanContext
}

// End 结束 span，并把完整记录追加到日志文件。
func (s *span) End(options ...trace.SpanEndOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return
	}

	cfg := trace.NewSpanEndConfig(options...)
	end := time.Now().UTC()
	if !cfg.Timestamp().IsZero() {
		end = cfg.Timestamp()
	}
	s.endTime = end
	s.ended = true

	rec := &logRecord{
		TraceID:    s.traceID.String(),
		SpanID:     s.spanID.String(),
		Name:       s.name,
		StartTime:  s.startTime.Format(time.RFC3339Nano),
		EndTime:    s.endTime.Format(time.RFC3339Nano),
		DurationMs: float64(s.endTime.Sub(s.startTime)) / float64(time.Millisecond),
		Attributes: s.attrs,
		StatusCode: statusString(s.statusCode),
		StatusMsg:  s.statusMsg,
		Events:     s.events,
	}
	if s.parentSpanID.IsValid() {
		rec.ParentSpanID = s.parentSpanID.String()
	}

	if err := s.tracer.provider.appendLine(rec); err != nil {
		// 追踪失败不应影响主流程，只能落到 stderr。
		fmt.Fprintf(os.Stderr, "filetrace: %v\n", err)
	}
}

// AddEvent 记录一个事件。
func (s *span) AddEvent(name string, options ...trace.EventOption) {
	cfg := trace.NewEventConfig(options...)
	ts := time.Now().UTC()
	if !cfg.Timestamp().IsZero() {
		ts = cfg.Timestamp()
	}
	ev := eventRecord{
		Name:       name,
		Timestamp:  ts.Format(time.RFC3339Nano),
		Attributes: attrsToMap(cfg.Attributes()),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.events = append(s.events, ev)
}

// AddLink 当前不记录链接。
func (s *span) AddLink(_ trace.Link) {}

// IsRecording 返回 span 是否仍在记录中。
func (s *span) IsRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.ended
}

// RecordError 把错误记录为 exception 事件。
func (s *span) RecordError(err error, options ...trace.EventOption) {
	if err == nil {
		return
	}
	options = append(options, trace.WithAttributes(
		attribute.String("exception.message", err.Error()),
	))
	s.AddEvent("exception", options...)
}

// SpanContext 返回本 span 的上下文（含 trace_id / span_id）。
func (s *span) SpanContext() trace.SpanContext {
	return s.spanCtx
}

// SetName 允许在 span 结束前改名。
func (s *span) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.name = name
	}
}

// SetStatus 设置 span 状态。
func (s *span) SetStatus(c codes.Code, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.statusCode = c
	s.statusMsg = msg
}

// SetAttributes 合并属性；已存在的键会被覆盖。
func (s *span) SetAttributes(kvs ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	for _, kv := range kvs {
		s.attrs[string(kv.Key)] = kv.Value.AsInterface()
	}
}

// TracerProvider 返回本 span 所属的 Provider。
func (s *span) TracerProvider() trace.TracerProvider {
	return s.tracer.provider
}

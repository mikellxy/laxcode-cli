// Package custom 演示 laxcode 自定义 tracing Handle 的扩展规范：
// 把本文件复制到 internal/tracing/custom/ 后重新编译，启动时加
// -trace_hanle_name=otlp，即可把 span 以 OTLP/HTTP(JSON) 批量上报到
// Jaeger / Tempo / otel-collector 等协议兼容后端。
//
// 规范要点：
//  1. 自定义入口文件必须放在 internal/tracing/custom 包中——main.go 以
//     _ "github.com/mikellxy/laxcode/internal/tracing/custom" 空导入触发 init；
//  2. 在 init() 中调用 tracing.Register(name, tracing.New(provider)) 注册；
//  3. trace.TracerProvider / Tracer / Span 是密封接口（含未导出标记方法），
//     须内嵌 go.opentelemetry.io/otel/trace/embedded 包中的对应接口；
//  4. provider 若实现 Shutdown(context.Context) error，进程退出前会被
//     tracing.Handle.Shutdown 自动探测调用，用于 flush 尾部 span。
package custom

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// handleName 是注册名，启动参数 -trace_hanle_name=otlp 据此选中本实现
const handleName = "otlp"

func init() {
	endpoint := os.Getenv("LAXCODE_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4318/v1/traces"
	}
	service := os.Getenv("OTEL_SERVICE_NAME")
	if service == "" {
		service = "laxcode"
	}
	tracing.Register(handleName, tracing.New(newProvider(endpoint, service)))
}

// provider 在 span.End 时把快照写入队列，由后台 goroutine 批量 POST。
// worker 惰性启动：init 只注册不干活，仅当本 Handle 被选中并产生第一个
// span 时才启动后台 goroutine。上报失败只计数，绝不阻塞或 panic 主流程。
type provider struct {
	embedded.TracerProvider

	endpoint string
	service  string
	client   *http.Client
	queue    chan *spanData
	done     chan struct{}

	startOnce sync.Once
	closeOnce sync.Once
	started   atomic.Bool
	wg        sync.WaitGroup
	dropped   atomic.Uint64
}

func newProvider(endpoint, service string) *provider {
	return &provider{
		endpoint: endpoint,
		service:  service,
		client:   &http.Client{Timeout: 10 * time.Second},
		queue:    make(chan *spanData, 1024),
		done:     make(chan struct{}),
	}
}

func (p *provider) Tracer(_ string, _ ...trace.TracerOption) trace.Tracer {
	return &tracer{p: p}
}

// Shutdown 被 tracing.Handle.Shutdown 探测转发：关闭队列并等 worker 排空。
// 幂等；ctx 超时提前返回，不拖住进程退出。
func (p *provider) Shutdown(ctx context.Context) error {
	p.closeOnce.Do(func() { close(p.done) })
	if !p.started.Load() {
		return nil
	}
	wait := make(chan struct{})
	go func() { p.wg.Wait(); close(wait) }()
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------- Tracer ----------

type tracer struct {
	embedded.Tracer
	p *provider
}

func (t *tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	// 首个 span 出现时启动后台导出 worker
	t.p.startOnce.Do(func() {
		t.p.started.Store(true)
		t.p.wg.Add(1)
		go t.p.run()
	})

	cfg := trace.NewSpanStartConfig(opts...)
	start := cfg.Timestamp()
	if start.IsZero() {
		start = time.Now()
	}

	// 父子关系：ctx 中有有效父 span 则继承其 trace，否则开新 trace
	var traceID trace.TraceID
	var parentID trace.SpanID
	if parent := trace.SpanContextFromContext(ctx); parent.IsValid() && !cfg.NewRoot() {
		traceID, parentID = parent.TraceID(), parent.SpanID()
	} else {
		traceID = newTraceID()
	}

	s := &span{
		p:        t.p,
		name:     name,
		start:    start,
		parentID: parentID,
		attrs:    map[attribute.Key]attribute.Value{},
		sc: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     newSpanID(),
			TraceFlags: trace.FlagsSampled,
		}),
	}
	for _, kv := range cfg.Attributes() {
		s.attrs[kv.Key] = kv.Value
	}
	// 关键：把新 span 写回 ctx，下游埋点才能成为它的孩子
	return trace.ContextWithSpan(ctx, s), s
}

func newTraceID() (id trace.TraceID) { _, _ = rand.Read(id[:]); return }
func newSpanID() (id trace.SpanID)   { _, _ = rand.Read(id[:]); return }

// ---------- Span ----------

// span 记录全部写入端数据；并行工具调用会并发写，所有可变字段加锁。
// End 之后 laxcode 不再触碰 span，写入端方法一律转为 no-op。
type span struct {
	embedded.Span
	p *provider

	mu         sync.Mutex
	sc         trace.SpanContext
	parentID   trace.SpanID
	name       string
	start, end time.Time
	attrs      map[attribute.Key]attribute.Value
	statusCode codes.Code
	statusDesc string
	events     []eventData
	ended      bool
}

type eventData struct {
	name  string
	at    time.Time
	attrs []attribute.KeyValue
}

// End 是上报触发点：做快照并入队即返回，热路径不做任何网络 IO
func (s *span) End(opts ...trace.SpanEndOption) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	endCfg := trace.NewSpanEndConfig(opts...)
	s.end = endCfg.Timestamp()
	if s.end.IsZero() {
		s.end = time.Now()
	}
	data := s.snapshotLocked()
	s.mu.Unlock()

	select {
	case s.p.queue <- data:
	default: // 队列满丢弃并计数，绝不阻塞 agent 循环
		s.p.dropped.Add(1)
	}
}

func (s *span) SetAttributes(kvs ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	for _, kv := range kvs {
		s.attrs[kv.Key] = kv.Value
	}
}

func (s *span) SetStatus(c codes.Code, desc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.statusCode, s.statusDesc = c, desc
}

// RecordError 落成 OTLP exception event，后端会渲染为错误标记
func (s *span) RecordError(err error, _ ...trace.EventOption) {
	s.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.message", err.Error()),
	))
}

func (s *span) AddEvent(name string, opts ...trace.EventOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	cfg := trace.NewEventConfig(opts...)
	at := cfg.Timestamp()
	if at.IsZero() {
		at = time.Now()
	}
	s.events = append(s.events, eventData{name: name, at: at, attrs: cfg.Attributes()})
}

func (s *span) AddLink(trace.Link)                   {}
func (s *span) IsRecording() bool                    { return true }
func (s *span) SpanContext() trace.SpanContext       { return s.sc }
func (s *span) TracerProvider() trace.TracerProvider { return s.p }

func (s *span) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.name = name
	}
}

// ---------- 快照与 OTLP/JSON 编码 ----------

// spanData 是 End 时刻的不可变快照
type spanData struct {
	traceID, spanID, parentID string
	name                      string
	start, end                time.Time
	attrs                     map[attribute.Key]attribute.Value
	events                    []eventData
	statusCode                codes.Code
	statusDesc                string
}

func (s *span) snapshotLocked() *spanData {
	traceID, spanID := s.sc.TraceID(), s.sc.SpanID()
	d := &spanData{
		traceID:    hex.EncodeToString(traceID[:]), // OTLP/JSON 规定 hex，不是 base64
		spanID:     hex.EncodeToString(spanID[:]),
		name:       s.name,
		start:      s.start,
		end:        s.end,
		attrs:      s.attrs,
		events:     s.events,
		statusCode: s.statusCode,
		statusDesc: s.statusDesc,
	}
	if s.parentID.IsValid() {
		parentID := s.parentID
		d.parentID = hex.EncodeToString(parentID[:])
	}
	return d
}

func otlpValue(v attribute.Value) map[string]any {
	switch v.Type() {
	case attribute.BOOL:
		return map[string]any{"boolValue": v.AsBool()}
	case attribute.INT64: // OTLP/JSON 中 int64 编码为字符串
		return map[string]any{"intValue": strconv.FormatInt(v.AsInt64(), 10)}
	case attribute.FLOAT64:
		return map[string]any{"doubleValue": v.AsFloat64()}
	default:
		return map[string]any{"stringValue": v.AsString()}
	}
}

func otlpAttrs(m map[attribute.Key]attribute.Value) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		out = append(out, map[string]any{"key": string(k), "value": otlpValue(v)})
	}
	return out
}

// codes.Code → OTLP Status.code：Unset=0 / Ok=1 / Error=2
func otlpStatusCode(c codes.Code) int {
	switch c {
	case codes.Ok:
		return 1
	case codes.Error:
		return 2
	default:
		return 0
	}
}

// ---------- 批量导出 ----------

func (p *provider) run() {
	defer p.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var batch []*spanData
	flush := func() {
		if len(batch) > 0 {
			p.export(batch)
			batch = nil
		}
	}
	for {
		select {
		case d := <-p.queue:
			batch = append(batch, d)
			if len(batch) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-p.done:
			for { // 排空剩余后最后 flush 一次
				select {
				case d := <-p.queue:
					batch = append(batch, d)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (p *provider) export(batch []*spanData) {
	spans := make([]map[string]any, 0, len(batch))
	for _, d := range batch {
		sp := map[string]any{
			"traceId":           d.traceID,
			"spanId":            d.spanID,
			"name":              d.name,
			"kind":              1, // laxcode 未设置 SpanKind，恒为 internal
			"startTimeUnixNano": strconv.FormatInt(d.start.UnixNano(), 10),
			"endTimeUnixNano":   strconv.FormatInt(d.end.UnixNano(), 10),
			"attributes":        otlpAttrs(d.attrs),
			"status":            map[string]any{"code": otlpStatusCode(d.statusCode), "message": d.statusDesc},
		}
		if d.parentID != "" {
			sp["parentSpanId"] = d.parentID
		}
		if len(d.events) > 0 {
			evs := make([]map[string]any, 0, len(d.events))
			for _, e := range d.events {
				m := make(map[attribute.Key]attribute.Value, len(e.attrs))
				for _, kv := range e.attrs {
					m[kv.Key] = kv.Value
				}
				evs = append(evs, map[string]any{
					"timeUnixNano": strconv.FormatInt(e.at.UnixNano(), 10),
					"name":         e.name,
					"attributes":   otlpAttrs(m),
				})
			}
			sp["events"] = evs
		}
		spans = append(spans, sp)
	}

	payload := map[string]any{
		"resourceSpans": []map[string]any{{
			"resource": map[string]any{"attributes": []map[string]any{{
				"key": "service.name", "value": map[string]any{"stringValue": p.service},
			}}},
			"scopeSpans": []map[string]any{{
				"scope": map[string]any{"name": "github.com/mikellxy/laxcode"},
				"spans": spans,
			}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req) // 失败默认丢弃；需要可靠性可在此加重试
	if err == nil {
		_ = resp.Body.Close()
	}
}

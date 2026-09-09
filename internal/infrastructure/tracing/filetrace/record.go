package filetrace

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// logRecord 是写入日志文件的 JSON Lines 单条记录结构。
type logRecord struct {
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Name         string         `json:"name"`
	StartTime    string         `json:"start_time"`
	EndTime      string         `json:"end_time"`
	DurationMs   float64        `json:"duration_ms"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	StatusCode   string         `json:"status_code,omitempty"`
	StatusMsg    string         `json:"status_msg,omitempty"`
	Events       []eventRecord  `json:"events,omitempty"`
}

// eventRecord 是 span 上的事件/异常记录。
type eventRecord struct {
	Name       string         `json:"name"`
	Timestamp  string         `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// attrsToMap 把 OTel 属性列表转为 map[string]any，方便 JSON 序列化。
func attrsToMap(kvs []attribute.KeyValue) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value.AsInterface()
	}
	return m
}

// statusString 返回状态码的字符串表示，未设置时返回空串避免日志中出现 Unset。
func statusString(c codes.Code) string {
	if c == codes.Unset {
		return ""
	}
	return c.String()
}

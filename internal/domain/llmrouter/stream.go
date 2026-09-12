package llmrouter

import "context"

// InvalidRequestError 表示请求 JSON 合法，但不符合 OpenAI Responses 参数结构。
// HTTP adapter 可据此返回 400，而不会把调用方错误误报成上游 502。
type InvalidRequestError struct {
	Err error
}

func (e *InvalidRequestError) Error() string { return e.Err.Error() }
func (e *InvalidRequestError) Unwrap() error { return e.Err }

// StreamEvent 是上游 Responses API 的单个 SSE 事件。Data 保留 SDK 收到的
// 原始 JSON，application 层只负责封装 SSE 帧，不解释模型响应内容。
type StreamEvent struct {
	Type string
	Data []byte
}

// Stream 抽象一次进行中的模型流。调用方必须 Close；请求 context 取消时，
// 具体实现也应立即停止上游请求。
type Stream interface {
	Next() bool
	Current() StreamEvent
	Err() error
	Close() error
}

// StreamClient 是模型路由 HTTP 服务依赖的上游端口。requestJSON 使用 OpenAI
// Responses API 请求结构，由基础设施实现负责解析并调用真实 SDK。
type StreamClient interface {
	GenerateStream(ctx context.Context, requestJSON []byte) (Stream, error)
}

// Package run_sse 是 sse server 模式的前端入口：起一个标准库 net/http 服务，
// 接受 POST /chat（body 携带 session_id / task），为每个请求装配一个独立的
// ReActService，把 ReActEventConsumerF 收到的 ReAct 事件以细粒度语义 SSE 帧流式
// 回传，本轮结束发 done（成功）或 error（失败）帧收尾。它与交互模式
// （cmd/run_cli）、one-shot（cmd/run_oneshot）平级，共用 cmd/agentasm 组合根与
// application/reactservice 的 ReAct 循环，仅依赖 DDD 三层，不引用老的非 DDD 代码。
//
// 装配采用「每请求一次」：agentasm.Assemble 在装配时固定 Consumer 与 SessionID，
// 且 ReActService 绑定单一 Session，故要让每个请求把事件写到自己的 SSE 流、并支持
// 任意 session_id 续聊，只能每请求装配独立服务、跑一次 Chat 后 Cleanup（与 one-shot
// 同构，只是从进程级一次变为请求级一次）。
package run_sse

import (
	"encoding/json"
	"net/http"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// SSE 事件名：客户端按 event 名分流。reasoning / message 承载增量文本，
// tool_call 承载工具执行提示，start / done / error 承载会话生命周期与结果。
const (
	EventStart     = "start"
	EventReasoning = "reasoning"
	EventMessage   = "message"
	EventToolCall  = "tool_call"
	EventDone      = "done"
	EventError     = "error"
)

// StartData 是 start 帧载荷：装配成功后立即回传实际会话 id。新建会话（请求
// session_id 为空）时，客户端据此在后续请求里带回同一 id 续聊。
type StartData struct {
	SessionID string `json:"session_id"`
}

// DeltaData 是 reasoning / message 帧载荷：本次流式增量文本。
type DeltaData struct {
	Delta string `json:"delta"`
}

// ToolCallData 是 tool_call 帧载荷：工具执行提示（BeforeExecInfo 文本）。
type ToolCallData struct {
	Info string `json:"info"`
}

// DoneData 是 done 帧载荷：本轮最终结果与 token 账目。字段对齐 run_oneshot 的
// 成功语义（session_id / result / token_used / window_token），使两种模式的
// 结果契约一致，客户端可复用同一套解析。
type DoneData struct {
	SessionID   string                       `json:"session_id"`
	Result      string                       `json:"result"`
	TokenUsed   sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken sharedkernel.TokenStatistics `json:"window_token"`
}

// ErrorData 是 error 帧载荷：进入 SSE 流之后的失败（装配 / Chat）一律经它回传，
// 因为响应头已发送、HTTP 状态码无法再回退（流开始前的用法错误仍走普通 JSON +
// 状态码，见 handler）。
type ErrorData struct {
	Message string `json:"message"`
}

// sseWriter 把事件序列化为 SSE 帧写入 ResponseWriter 并立即 Flush，使客户端
// 逐帧收到而非等响应结束。
//
// 无需并发保护：ReActEventConsumerF 在 Chat 调用 goroutine 内同步触发
// （GenerateStream 的 emit 回调与工具执行前提示均同步），handler 又在 Chat 返回后
// 于同一 goroutine 发 done / error，全程单 goroutine 顺序访问；子 Agent 事件静默
// 丢弃，不入本流。
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSEWriter(w http.ResponseWriter, f http.Flusher) *sseWriter {
	return &sseWriter{w: w, f: f}
}

// Send 写一帧 "event: <name>\ndata: <json>\n\n" 并 Flush。data 序列化失败或
// 写入失败（多为客户端已断开）时静默跳过：SSE 已无法回退状态码，且断连会经
// r.Context() 取消驱动 Chat 收敛，无需在此中断流。
func (s *sseWriter) Send(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = s.w.Write([]byte("event: " + event + "\ndata: " + string(payload) + "\n\n"))
	s.f.Flush()
}

// newEventConsumer 返回把 ReAct 事件映射为 SSE 帧的回调，作为 Consumer 注入
// cmd/agentasm 的装配。映射规则对齐 run_cli 的呈现语义：
//   - reasoning / text 增量分别推 reasoning / message 帧；
//   - 三段式的 start / end 边界不单独发帧（event 名切换已隐含段落边界，协议精简）；
//   - ChunkToolCall（参数就绪）静默，等 ReActEventTypeToolCall 的执行提示帧，
//     与 run_cli「参数就绪不显示、执行前才显示」一致。
func newEventConsumer(sw *sseWriter) func(*reactservice.ReactEvent) {
	return func(e *reactservice.ReactEvent) {
		switch e.Type {
		case reactservice.ReActEventTypeChunk:
			chunk := e.ChunkEvent
			if chunk == nil {
				return
			}
			switch chunk.Kind {
			case sharedkernel.ChunkReasoningDelta:
				sw.Send(EventReasoning, DeltaData{Delta: chunk.Delta})
			case sharedkernel.ChunkTextDelta:
				sw.Send(EventMessage, DeltaData{Delta: chunk.Delta})
			}
		case reactservice.ReActEventTypeToolCall:
			sw.Send(EventToolCall, ToolCallData{Info: e.Content})
		}
	}
}

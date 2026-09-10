package run_sse

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// recordFlusher 是实现 http.ResponseWriter + http.Flusher 的测试替身：写入累积到
// buf，并记录 Flush 次数，供断言「每帧一次 Flush」的逐帧下发行为。
type recordFlusher struct {
	header  http.Header
	buf     bytes.Buffer
	flushes int
}

func newRecordFlusher() *recordFlusher {
	return &recordFlusher{header: http.Header{}}
}

func (r *recordFlusher) Header() http.Header         { return r.header }
func (r *recordFlusher) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recordFlusher) WriteHeader(int)             {}
func (r *recordFlusher) Flush()                      { r.flushes++ }

// TestSSEWriterSendFrame 验证单帧的 SSE 线格式：event 行 + data 行（JSON）+ 空行，
// 且每次 Send 触发一次 Flush。
func TestSSEWriterSendFrame(t *testing.T) {
	rf := newRecordFlusher()
	sw := newSSEWriter(rf, rf)
	sw.Send(EventMessage, DeltaData{Delta: "hi"})

	want := "event: message\ndata: {\"delta\":\"hi\"}\n\n"
	if rf.buf.String() != want {
		t.Fatalf("帧格式不符：got %q, want %q", rf.buf.String(), want)
	}
	if rf.flushes != 1 {
		t.Fatalf("每帧应 Flush 一次，实际 %d 次", rf.flushes)
	}
}

// TestEventConsumerMapsToSSEFrames 验证 ReactEvent → SSE 帧的映射：reasoning/text
// 增量各推一帧，三段式的 start/end 与 ChunkToolCall（参数就绪）静默不发帧，工具执行
// 提示推 tool_call 帧。与 run_cli 的呈现语义一致。
func TestEventConsumerMapsToSSEFrames(t *testing.T) {
	rf := newRecordFlusher()
	rcf := newEventConsumer(newSSEWriter(rf, rf))

	chunks := []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkReasoningStart},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "先思考"},
		{Kind: sharedkernel.ChunkReasoningEnd},
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "hello"},
		{Kind: sharedkernel.ChunkTextDelta, Delta: " world"},
		{Kind: sharedkernel.ChunkTextEnd},
		{Kind: sharedkernel.ChunkToolCall, ToolCall: &sharedkernel.ToolCall{Name: "bash"}},
	}
	for i := range chunks {
		rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &chunks[i]})
	}
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeToolCall, Content: "bash: ls"})

	want := "event: reasoning\ndata: {\"delta\":\"先思考\"}\n\n" +
		"event: message\ndata: {\"delta\":\"hello\"}\n\n" +
		"event: message\ndata: {\"delta\":\" world\"}\n\n" +
		"event: tool_call\ndata: {\"info\":\"bash: ls\"}\n\n"
	if rf.buf.String() != want {
		t.Fatalf("SSE 帧序列不符：\ngot:  %q\nwant: %q", rf.buf.String(), want)
	}
	// reasoning(1) + message(2) + tool_call(1) = 4 帧，各 Flush 一次；
	// start/end 边界与 ChunkToolCall 不发帧、不 Flush。
	if rf.flushes != 4 {
		t.Fatalf("应 Flush 4 次（4 帧），实际 %d 次", rf.flushes)
	}
}

// TestEventConsumerNilChunkIgnored 验证 chunk 事件缺 ChunkEvent 时安全跳过，
// 不产生任何帧（对齐 run_cli 的 nil 保护）。
func TestEventConsumerNilChunkIgnored(t *testing.T) {
	rf := newRecordFlusher()
	rcf := newEventConsumer(newSSEWriter(rf, rf))
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk}) // ChunkEvent 为 nil

	if rf.buf.Len() != 0 || rf.flushes != 0 {
		t.Fatalf("nil chunk 不应产生帧：%q, flushes=%d", rf.buf.String(), rf.flushes)
	}
}

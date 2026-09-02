package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// 编译期断言：*OpenApiProvider 实现可选流式接口 StreamProvider。
var _ StreamProvider = (*OpenApiProvider)(nil)

// newStreamTestProvider 构造一个 client 指向假 SSE 服务的 OpenApiProvider，
// 绕开包级 config 变量。handler 对任意路径返回同一 canned SSE 正文。
func newStreamTestProvider(sseBody string) (*OpenApiProvider, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	p := &OpenApiProvider{
		client: openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/v1/")),
		model:  "test-model",
		info:   Info{Name: "test"},
	}
	return p, srv
}

// fullSSEBody 覆盖 reasoning 三段式、正文三段式、一个完整工具调用与终止用量事件。
const fullSSEBody = `event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","delta":"Let me ","item_id":"rs_1","output_index":0,"sequence_number":1}

event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","delta":"think.","item_id":"rs_1","output_index":0,"sequence_number":2}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"sequence_number":3,"item":{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"Let me think."}]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello","item_id":"msg_1","output_index":1,"content_index":0,"sequence_number":4}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" world","item_id":"msg_1","output_index":1,"content_index":0,"sequence_number":5}

event: response.output_text.done
data: {"type":"response.output_text.done","text":"Hello world","item_id":"msg_1","output_index":1,"content_index":0,"sequence_number":6}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"{\"cmd\":","item_id":"fc_1","output_index":2,"sequence_number":7}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":2,"sequence_number":8,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}","status":"completed"}}

event: response.completed
data: {"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response","created_at":0,"model":"test-model","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`

// TestOpenAIProvider_GenerateStream_ThreePhaseAndEquivalence 验证 3.2/3.3：
// emit 顺序符合三段式，且返回消息与增量累积语义等价。
func TestOpenAIProvider_GenerateStream_ThreePhaseAndEquivalence(t *testing.T) {
	p, srv := newStreamTestProvider(fullSSEBody)
	defer srv.Close()

	var got []StreamChunk
	msg, err := p.GenerateStream(context.Background(),
		[]schema.Message{{Role: schema.RoleUser, Content: "hi"}},
		nil,
		func(c StreamChunk) { got = append(got, c) })
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	// 3.2 emit 顺序：reasoning 三段式 → 正文三段式 → 完整工具调用
	wantKinds := []ChunkKind{
		ChunkReasoningStart, ChunkReasoningDelta, ChunkReasoningDelta, ChunkReasoningEnd,
		ChunkTextStart, ChunkTextDelta, ChunkTextDelta, ChunkTextEnd,
		ChunkToolCall,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("emit count = %d, want %d (kinds=%v)", len(got), len(wantKinds), kindsOf(got))
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Errorf("emit[%d].Kind = %d, want %d", i, got[i].Kind, want)
		}
	}

	// 3.3 正文等价：返回 Content == 全部文本 delta 依序拼接
	var textDelta string
	for _, c := range got {
		if c.Kind == ChunkTextDelta {
			textDelta += c.Delta
		}
	}
	if msg.Content != textDelta {
		t.Errorf("msg.Content = %q, want concatenated text deltas %q", msg.Content, textDelta)
	}
	if msg.Content != "Hello world" {
		t.Errorf("msg.Content = %q, want %q", msg.Content, "Hello world")
	}

	// reasoning 累积取自 output_item.done 的权威 item
	if msg.ReasoningContent != "Let me think." {
		t.Errorf("msg.ReasoningContent = %q, want %q", msg.ReasoningContent, "Let me think.")
	}
	if msg.ReasoningID != "rs_1" {
		t.Errorf("msg.ReasoningID = %q, want %q", msg.ReasoningID, "rs_1")
	}

	// token 用量取自 completed 事件
	if msg.TokenUsed.TokenInput != 10 || msg.TokenUsed.TokenOutput != 5 {
		t.Errorf("msg.TokenUsed = %+v, want {Input:10 Output:5}", msg.TokenUsed)
	}

	// 工具调用：返回消息与 emit 的 ChunkToolCall 一致
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("msg.ToolCalls len = %d, want 1", len(msg.ToolCalls))
	}
	var emittedToolCalls []*schema.ToolCall
	for _, c := range got {
		if c.Kind == ChunkToolCall {
			emittedToolCalls = append(emittedToolCalls, c.ToolCall)
		}
	}
	if len(emittedToolCalls) != 1 {
		t.Fatalf("emitted ChunkToolCall count = %d, want 1", len(emittedToolCalls))
	}
	if emittedToolCalls[0].ID != msg.ToolCalls[0].ID ||
		emittedToolCalls[0].Name != msg.ToolCalls[0].Name ||
		string(emittedToolCalls[0].Arguments) != string(msg.ToolCalls[0].Arguments) {
		t.Errorf("emitted tool call %+v != msg tool call %+v", emittedToolCalls[0], msg.ToolCalls[0])
	}
}

// TestOpenAIProvider_GenerateStream_ToolCallNotStreamed 验证 3.4：
// 工具调用只以完整事件推送，参数片段 delta 不产生任何 emit，且参数是完整合法 JSON。
func TestOpenAIProvider_GenerateStream_ToolCallNotStreamed(t *testing.T) {
	p, srv := newStreamTestProvider(fullSSEBody)
	defer srv.Close()

	var got []StreamChunk
	if _, err := p.GenerateStream(context.Background(), nil, nil, func(c StreamChunk) {
		got = append(got, c)
	}); err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	var toolCalls []StreamChunk
	for _, c := range got {
		if c.Kind == ChunkToolCall {
			toolCalls = append(toolCalls, c)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("ChunkToolCall count = %d, want 1 (partial-arg events must not be emitted)", len(toolCalls))
	}
	args := toolCalls[0].ToolCall.Arguments
	if !json.Valid(args) {
		t.Errorf("tool call arguments %q is not complete valid JSON", string(args))
	}
	if string(args) != `{"cmd":"ls"}` {
		t.Errorf("tool call arguments = %s, want %s", string(args), `{"cmd":"ls"}`)
	}
	// 参数片段 delta 不得混入任何文本/reasoning 增量事件
	for _, c := range got {
		if (c.Kind == ChunkTextDelta || c.Kind == ChunkReasoningDelta) && c.Delta == `{"cmd":` {
			t.Errorf("partial tool-arg delta leaked into stream events: %q", c.Delta)
		}
	}
}

// TestStreamProvider_CapabilityDetection 验证 3.5：能力探测区分流式与批式。
func TestStreamProvider_CapabilityDetection(t *testing.T) {
	// OpenApiProvider 满足 StreamProvider（另有编译期断言兜底）
	var openaiP Provider = &OpenApiProvider{}
	if _, ok := openaiP.(StreamProvider); !ok {
		t.Errorf("*OpenApiProvider should satisfy StreamProvider")
	}
	// AnthropicProvider 不实现流式，探测应为 false，可继续走批式
	var anthropicP Provider = NewAnthropicProvider(Info{Name: "test"})
	if _, ok := anthropicP.(StreamProvider); ok {
		t.Errorf("*AnthropicProvider should NOT satisfy StreamProvider")
	}
}

// TestOpenAIProvider_GenerateStream_MidStreamError 验证 3.6：流中途出错时
// 停止 emit 并返回非 nil 错误。
func TestOpenAIProvider_GenerateStream_MidStreamError(t *testing.T) {
	body := `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"partial","item_id":"msg_1","output_index":0,"content_index":0,"sequence_number":1}

data: {"error":{"message":"boom","type":"server_error"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"AFTER_ERROR","item_id":"msg_1","output_index":0,"content_index":0,"sequence_number":3}

`
	p, srv := newStreamTestProvider(body)
	defer srv.Close()

	var got []StreamChunk
	msg, err := p.GenerateStream(context.Background(), nil, nil, func(c StreamChunk) {
		got = append(got, c)
	})
	if err == nil {
		t.Fatalf("GenerateStream() expected error, got nil (msg=%+v)", msg)
	}
	if msg != nil {
		t.Errorf("GenerateStream() msg = %+v, want nil on error", msg)
	}
	// 错误后不再推送任何事件
	for _, c := range got {
		if c.Delta == "AFTER_ERROR" {
			t.Errorf("events emitted after mid-stream error: %q", c.Delta)
		}
	}
}

// TestOpenAIProvider_GenerateStream_CancelledContext 验证 3.6：上下文取消时
// 返回反映取消/请求失败的错误，不阻塞。
func TestOpenAIProvider_GenerateStream_CancelledContext(t *testing.T) {
	p, srv := newStreamTestProvider(fullSSEBody)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 调用前即取消

	emitted := 0
	msg, err := p.GenerateStream(ctx, nil, nil, func(c StreamChunk) { emitted++ })
	if err == nil {
		t.Fatalf("GenerateStream() with cancelled ctx expected error, got nil (msg=%+v)", msg)
	}
}

func kindsOf(chunks []StreamChunk) []ChunkKind {
	out := make([]ChunkKind, len(chunks))
	for i, c := range chunks {
		out[i] = c.Kind
	}
	return out
}

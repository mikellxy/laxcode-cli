package run_sse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/cmd/agentasm"
)

// TestHandleChatInvalidJSON 验证非法请求体在进入 SSE 流之前返回 400 + JSON。
func TestHandleChatInvalidJSON(t *testing.T) {
	s := newServer(t.TempDir(), false)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("流前错误应为 JSON，实际 Content-Type %q", ct)
	}
}

// TestHandleChatEmptyTask 验证 task 为空（含纯空白）返回 400。
func TestHandleChatEmptyTask(t *testing.T) {
	s := newServer(t.TempDir(), false)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"s1","task":"   "}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 task 应 400，实际 %d", rec.Code)
	}
}

// TestHandleChatSessionBusy 验证同一 session_id 已被占用时返回 409（不排队等待），
// 避免同会话并发导致 history/meta 分叉，也避免客户端无感挂起。
func TestHandleChatSessionBusy(t *testing.T) {
	s := newServer(t.TempDir(), false)
	unlock, ok := s.locks.TryLock("sess-1") // 预占，模拟同会话并发
	if !ok {
		t.Fatal("预占 session 锁失败")
	}
	defer unlock()

	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"sess-1","task":"hi"}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("同 session 并发应 409，实际 %d", rec.Code)
	}
}

// nonFlusherWriter 是不实现 http.Flusher 的 ResponseWriter，用于验证无法流式时的降级。
type nonFlusherWriter struct {
	header http.Header
	code   int
}

func newNonFlusherWriter() *nonFlusherWriter {
	return &nonFlusherWriter{header: http.Header{}}
}

func (n *nonFlusherWriter) Header() http.Header         { return n.header }
func (n *nonFlusherWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusherWriter) WriteHeader(code int)        { n.code = code }

// TestHandleChatNoFlusher 验证 ResponseWriter 不支持 Flusher 时返回 500（仍在进入流之前）。
func TestHandleChatNoFlusher(t *testing.T) {
	s := newServer(t.TempDir(), false)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"task":"hi"}`))
	w := newNonFlusherWriter()
	s.handleChat(w, req)

	if w.code != http.StatusInternalServerError {
		t.Fatalf("不支持流式应 500，实际 %d", w.code)
	}
}

// TestHandleChatAssembleError 验证装配失败时已进入 SSE 流（状态码固定 200、
// Content-Type 为 event-stream），错误经 event: error 帧回传，且此前不发 start 帧。
func TestHandleChatAssembleError(t *testing.T) {
	s := newServer(t.TempDir(), false)
	s.assemble = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
		return nil, errors.New("boom")
	}
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"task":"hi"}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("进入 SSE 流后状态码应为 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("应为 SSE Content-Type，实际 %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "boom") {
		t.Fatalf("装配失败应发含原因的 error 帧，实际 %q", body)
	}
	if strings.Contains(body, "event: start") {
		t.Fatalf("装配失败不应发 start 帧：%q", body)
	}
}

// TestSessionLocksSerializesSameID 验证锁表语义：同 id 二次 TryLock 失败，
// 释放后可再获取；不同 id 互不阻塞。
func TestSessionLocksSerializesSameID(t *testing.T) {
	locks := newSessionLocks()

	unlockA, ok := locks.TryLock("a")
	if !ok {
		t.Fatal("首次锁 a 应成功")
	}
	if _, ok := locks.TryLock("a"); ok {
		t.Fatal("a 已占用，二次 TryLock 应失败")
	}
	if _, ok := locks.TryLock("b"); !ok {
		t.Fatal("不同 id b 不应被 a 阻塞")
	}
	unlockA()
	if _, ok := locks.TryLock("a"); !ok {
		t.Fatal("a 释放后应可再次获取")
	}
}

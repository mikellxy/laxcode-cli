package run_sse

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/mikellxy/laxcode/cmd/agentasm"
)

// chatRequest 是 POST /chat 的请求体：session_id 为空则新建会话，task 必填非空。
type chatRequest struct {
	SessionID string `json:"session_id"`
	Task      string `json:"task"`
}

// maxBodyBytes 限制请求体大小，防止超大 body 耗尽内存。
const maxBodyBytes = 1 << 20

// server 承载 sse 模式的 HTTP 编排。assemble 字段默认 agentasm.Assemble，测试可
// 注入 fake 以覆盖装配失败 / 完整流路径而不依赖真实 LLM provider。
type server struct {
	workDir  string
	planMode bool
	assemble func(context.Context, agentasm.Input) (*agentasm.Assembled, error)
	locks    *sessionLocks
}

func newServer(workDir string, planMode bool) *server {
	return &server{
		workDir:  workDir,
		planMode: planMode,
		assemble: agentasm.Assemble,
		locks:    newSessionLocks(),
	}
}

// handleChat 处理 POST /chat：解析请求 → 同会话互斥 → 写 SSE 头 → 每请求装配 →
// 发 start 帧 → 跑 Chat（其间 Consumer 逐帧推 reasoning/message/tool_call）→ 发
// done/error 帧 → Cleanup。
//
// 错误分界：写 SSE 头之前的用法错误走普通 JSON + HTTP 状态码（400/409/500）；
// 一旦进入 SSE 流（响应头已发送），失败一律走 event: error 帧，状态码无法再回退。
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeJSONError(w, http.StatusBadRequest, "task is required")
		return
	}

	// 同会话串行：防止两个请求同时 InitSession→追加导致 history/meta 分叉。
	// 冲突返回 409 而非排队，避免客户端无感挂起；空 session 每次新建独立会话，无需锁。
	if req.SessionID != "" {
		unlock, ok := s.locks.TryLock(req.SessionID)
		if !ok {
			writeJSONError(w, http.StatusConflict, "session is busy: "+req.SessionID)
			return
		}
		defer unlock()
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// 进入 SSE 流：响应头一经发送状态码即固定，此后错误只走 event 帧。
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 禁反向代理缓冲，保证逐帧下发
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := newSSEWriter(w, flusher)
	ctx := r.Context() // 客户端断开即取消，驱动 Chat 从 LLM/工具调用收敛

	assembled, err := s.assemble(ctx, agentasm.Input{
		WorkDir:   s.workDir,
		SessionID: req.SessionID,
		PlanMode:  s.planMode,
		Consumer:  newEventConsumer(sw),
	})
	if err != nil {
		sw.Send(EventError, ErrorData{Message: "assemble agent failed: " + err.Error()})
		return
	}
	defer assembled.Cleanup()

	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	msg, err := assembled.Service.Chat(ctx, req.Task)
	if err != nil {
		sw.Send(EventError, ErrorData{Message: err.Error()})
		return
	}
	done := DoneData{
		SessionID:   assembled.Session.ID,
		TokenUsed:   assembled.Session.TokenUsed,
		WindowToken: assembled.Session.WindowToken,
	}
	if msg != nil {
		done.Result = msg.Content
	}
	sw.Send(EventDone, done)
}

// handleHealthz 是探活端点：返回 200，供负载均衡 / 容器健康检查，不触发装配。
func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// writeJSONError 写一个普通 JSON 错误响应，仅用于 SSE 流开始之前的用法错误
// （此时响应头未发送，可自由设置状态码）。载荷与 error 帧同为 {message}。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorData{Message: msg})
}

// sessionLocks 是 per-session 互斥锁表：同一 session_id 串行、不同 session 并发。
type sessionLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newSessionLocks() *sessionLocks {
	return &sessionLocks{m: make(map[string]*sync.Mutex)}
}

// TryLock 尝试锁定 id：成功返回 unlock 与 true；已被占用则立即返回 nil 与 false
// （不排队）。锁惰性创建，进程生命周期内不回收（session 数量有限，可接受）。
func (s *sessionLocks) TryLock(id string) (func(), bool) {
	s.mu.Lock()
	l, ok := s.m[id]
	if !ok {
		l = &sync.Mutex{}
		s.m[id] = l
	}
	s.mu.Unlock()

	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

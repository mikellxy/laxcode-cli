package llmrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
)

const (
	GenerateStreamPath  = "/openai/generate_stream"
	maxRequestBodyBytes = 16 << 20
)

// HTTPServer 把 OpenAI Responses 请求转交给配置好的上游 StreamClient，并将
// SDK 收到的事件恢复为标准 SSE 帧。实际监听端口和配置读取留在 cmd 组合根。
type HTTPServer struct {
	client domainrouter.StreamClient
	logger *slog.Logger
}

func NewHTTPServer(client domainrouter.StreamClient, loggers ...*slog.Logger) *HTTPServer {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &HTTPServer{client: client, logger: logger}
}

// RegisterRoutes 将模型网关端点挂到调用方提供的 mux。
func (s *HTTPServer) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST "+GenerateStreamPath, http.HandlerFunc(s.handleGenerateStream))
}

// RunningServer 是后台运行的本地模型路由器。Endpoint 返回供 OpenApiProvider
// 调用的完整 generate_stream URL；Shutdown 用于进程退出时停止接收新请求。
type RunningServer struct {
	server   *http.Server
	listener net.Listener
	endpoint string
}

func (s *RunningServer) Endpoint() string { return s.endpoint }

func (s *RunningServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Start 在 addr 上监听并以 goroutine 启动 HTTP server。推荐传 127.0.0.1:0，
// 让操作系统为每个 laxcode 进程分配独立端口。
func (s *HTTPServer) Start(addr string) (*RunningServer, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen LLM router on %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	running := &RunningServer{
		server:   server,
		listener: listener,
		endpoint: "http://" + listener.Addr().String() + GenerateStreamPath,
	}

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("llmrouter_server_failed", "error", err)
		}
	}()
	s.logger.Info("llmrouter_server_started", "addr", listener.Addr().String())
	return running, nil
}

// handleGenerateStream 接收 OpenAI Responses API JSON。它在提交 HTTP 200 前
// 预取第一个上游事件，因此鉴权失败、限流等上游 HTTP 错误仍能保留原状态码；
// 流开始后的网络错误则以 type=error 的 SSE 事件结束。
func (s *HTTPServer) handleGenerateStream(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	requestBodyBytes := int64(0)
	statusCode := http.StatusInternalServerError
	defer func() {
		s.logger.Info("llmrouter_request",
			"request_body_bytes", requestBodyBytes,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"status_code", statusCode,
		)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		statusCode = http.StatusInternalServerError
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if s.client == nil {
		statusCode = http.StatusServiceUnavailable
		writeJSONError(w, http.StatusServiceUnavailable, "LLM upstream is not configured")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	requestBodyBytes = int64(len(body))
	if err != nil {
		statusCode = http.StatusBadRequest
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || !json.Valid(body) || body[0] != '{' {
		statusCode = http.StatusBadRequest
		writeJSONError(w, http.StatusBadRequest, "request body must be a JSON object")
		return
	}

	stream, err := s.client.GenerateStream(r.Context(), body)
	if err != nil {
		var invalidRequest *domainrouter.InvalidRequestError
		if errors.As(err, &invalidRequest) {
			statusCode = http.StatusBadRequest
			writeJSONError(w, http.StatusBadRequest, invalidRequest.Error())
			return
		}
		statusCode = http.StatusBadGateway
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer stream.Close()

	// NewStreaming 在 SDK 中延迟暴露请求错误；先 Next 一次，确保非 2xx 上游
	// 响应能在本服务尚未写出 SSE header 时原样映射。
	if !stream.Next() {
		if err := stream.Err(); err != nil {
			statusCode = writePreStreamError(w, err)
			return
		}
		statusCode = http.StatusBadGateway
		writeJSONError(w, http.StatusBadGateway, "upstream returned an empty stream")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	statusCode = http.StatusOK
	w.WriteHeader(http.StatusOK)

	if err := writeEvent(w, stream.Current()); err != nil {
		return
	}
	flusher.Flush()

	for stream.Next() {
		if err := writeEvent(w, stream.Current()); err != nil {
			return
		}
		flusher.Flush()
	}
	if err := stream.Err(); err != nil && r.Context().Err() == nil {
		_ = writeGatewayStreamError(w, err)
		flusher.Flush()
	}
}

func writeEvent(w io.Writer, event domainrouter.StreamEvent) error {
	if event.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	// SSE 要求多行 payload 的每一行都带 data: 前缀。OpenAI 通常返回单行
	// JSON，这里仍完整处理多行以保持协议正确。
	for _, line := range bytes.Split(event.Data, []byte("\n")) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func writeGatewayStreamError(w io.Writer, streamErr error) error {
	data, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "gateway_stream_error",
			"message": streamErr.Error(),
		},
	})
	return writeEvent(w, domainrouter.StreamEvent{Type: "error", Data: data})
}

type upstreamHTTPError interface {
	error
	HTTPStatusCode() int
	ResponseBody() []byte
}

func writePreStreamError(w http.ResponseWriter, streamErr error) int {
	var upstreamErr upstreamHTTPError
	if errors.As(streamErr, &upstreamErr) {
		status := upstreamErr.HTTPStatusCode()
		body := upstreamErr.ResponseBody()
		if status >= 400 && status <= 599 && json.Valid(body) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return status
		}
	}
	writeJSONError(w, http.StatusBadGateway, streamErr.Error())
	return http.StatusBadGateway
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    "gateway_error",
			"message": message,
		},
	})
}

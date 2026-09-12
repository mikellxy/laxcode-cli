package llmrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
)

type fakeClient struct {
	request []byte
	stream  domainrouter.Stream
	err     error
	calls   int
}

func (f *fakeClient) GenerateStream(_ context.Context, request []byte) (domainrouter.Stream, error) {
	f.calls++
	f.request = append([]byte(nil), request...)
	return f.stream, f.err
}

type fakeStream struct {
	events []domainrouter.StreamEvent
	next   int
	cur    domainrouter.StreamEvent
	err    error
	closed bool
}

func (s *fakeStream) Next() bool {
	if s.next >= len(s.events) {
		return false
	}
	s.cur = s.events[s.next]
	s.next++
	return true
}

func (s *fakeStream) Current() domainrouter.StreamEvent { return s.cur }
func (s *fakeStream) Err() error                        { return s.err }
func (s *fakeStream) Close() error {
	s.closed = true
	return nil
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *flushRecorder) Flush() { r.flushes++ }

func TestGenerateStreamForwardsSSEEvents(t *testing.T) {
	stream := &fakeStream{events: []domainrouter.StreamEvent{
		{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"hi"}`)},
		{Type: "response.completed", Data: []byte(`{"type":"response.completed"}`)},
	}}
	client := &fakeClient{stream: stream}
	server := NewHTTPServer(client)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, GenerateStreamPath,
		strings.NewReader(`{"model":"ignored","input":"hello"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !bytes.Equal(client.request, []byte(`{"model":"ignored","input":"hello"}`)) {
		t.Fatalf("request was not forwarded: %s", client.request)
	}
	wantParts := []string{
		"event: response.output_text.delta\n",
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"event: response.completed\n",
	}
	for _, want := range wantParts {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response does not contain %q: %s", want, rec.Body.String())
		}
	}
	if rec.flushes != 2 {
		t.Fatalf("flushes = %d, want one per event", rec.flushes)
	}
	if !stream.closed {
		t.Fatal("upstream stream was not closed")
	}
}

func TestGenerateStreamWritesRequestMetricsLog(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	stream := &fakeStream{events: []domainrouter.StreamEvent{
		{Type: "response.completed", Data: []byte(`{"type":"response.completed"}`)},
	}}
	server := NewHTTPServer(&fakeClient{stream: stream}, logger)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	body := `{"input":"hello"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, GenerateStreamPath, strings.NewReader(body))
	mux.ServeHTTP(rec, req)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logBuffer.Bytes()), &record); err != nil {
		t.Fatalf("log is not valid JSON: %v; log=%s", err, logBuffer.String())
	}
	if record["msg"] != "llmrouter_request" || record["status_code"] != float64(http.StatusOK) {
		t.Fatalf("unexpected log record: %+v", record)
	}
	if record["request_body_bytes"] != float64(len(body)) {
		t.Fatalf("request_body_bytes = %v, want %d", record["request_body_bytes"], len(body))
	}
	if _, ok := record["duration_ms"]; !ok {
		t.Fatalf("duration_ms missing: %+v", record)
	}
	if _, ok := record["time"]; !ok {
		t.Fatalf("time missing: %+v", record)
	}
}

func TestGenerateStreamRejectsInvalidJSONBeforeCallingUpstream(t *testing.T) {
	client := &fakeClient{}
	server := NewHTTPServer(client)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, GenerateStreamPath, strings.NewReader(`not-json`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if client.calls != 0 {
		t.Fatalf("invalid request called upstream %d times", client.calls)
	}
}

type fakeUpstreamError struct{}

func (fakeUpstreamError) Error() string       { return "rate limited" }
func (fakeUpstreamError) HTTPStatusCode() int { return http.StatusTooManyRequests }
func (fakeUpstreamError) ResponseBody() []byte {
	return []byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`)
}

func TestGenerateStreamPreservesUpstreamHTTPErrorBeforeStreamStarts(t *testing.T) {
	stream := &fakeStream{err: fakeUpstreamError{}}
	server := NewHTTPServer(&fakeClient{stream: stream})
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, GenerateStreamPath, strings.NewReader(`{"input":"hello"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("upstream error body was not preserved: %s", rec.Body.String())
	}
}

func TestGenerateStreamWritesSSEErrorAfterPartialStream(t *testing.T) {
	stream := &fakeStream{
		events: []domainrouter.StreamEvent{{Type: "response.created", Data: []byte(`{"type":"response.created"}`)}},
		err:    errors.New("connection reset"),
	}
	server := NewHTTPServer(&fakeClient{stream: stream})
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, GenerateStreamPath, strings.NewReader(`{"input":"hello"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "gateway_stream_error") ||
		!strings.Contains(rec.Body.String(), "connection reset") {
		t.Fatalf("missing stream error event: %s", rec.Body.String())
	}
}

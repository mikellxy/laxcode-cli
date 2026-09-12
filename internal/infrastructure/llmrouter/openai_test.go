package llmrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"
)

func TestOpenAIStreamClientUsesConfiguredUpstreamAndModel(t *testing.T) {
	received := make(chan map[string]any, 1)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		received <- body

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"sequence_number\":1,\"item_id\":\"item-1\",\"output_index\":0,\"content_index\":0}\n\n")),
			Request: r,
		}, nil
	})
	httpClient := &http.Client{Transport: transport}

	client := newOpenAIStreamClient("upstream-secret", "https://upstream.example/v1", "configured-model",
		option.WithHTTPClient(httpClient))
	stream, err := client.GenerateStream(context.Background(), []byte(
		`{"model":"caller-model","input":[{"role":"user","content":"hi"}],"custom_provider":{"cache":true}}`))
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	defer stream.Close()

	if !stream.Next() {
		t.Fatalf("stream.Next=false: %v", stream.Err())
	}
	event := stream.Current()
	if event.Type != "response.output_text.delta" {
		t.Fatalf("event type = %q", event.Type)
	}
	if string(event.Data) != `{"type":"response.output_text.delta","delta":"hello","sequence_number":1,"item_id":"item-1","output_index":0,"content_index":0}` {
		t.Fatalf("event JSON was not preserved: %s", event.Data)
	}

	body := <-received
	if body["model"] != "configured-model" {
		t.Fatalf("model = %v, want configured-model", body["model"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input was lost during proxying: %v", body)
	}
	custom, ok := body["custom_provider"].(map[string]any)
	if !ok || custom["cache"] != true {
		t.Fatalf("custom provider body was not preserved: %v", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOpenAIStreamClientRejectsInvalidRequest(t *testing.T) {
	client := NewOpenAIStreamClient("key", "http://127.0.0.1:1", "model")
	if _, err := client.GenerateStream(context.Background(), []byte(`{"input":`)); err == nil {
		t.Fatal("invalid request must fail before calling upstream")
	}
}

func TestOpenAIStreamPreservesUpstreamHTTPError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit","param":null}}`)),
			Request: r,
		}, nil
	})
	client := newOpenAIStreamClient("key", "https://upstream.example/v1", "model",
		option.WithHTTPClient(&http.Client{Transport: transport}))
	stream, err := client.GenerateStream(context.Background(), []byte(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	defer stream.Close()

	if stream.Next() {
		t.Fatal("error response must not produce a stream event")
	}
	type httpError interface {
		HTTPStatusCode() int
		ResponseBody() []byte
	}
	got, ok := stream.Err().(httpError)
	if !ok {
		t.Fatalf("error does not expose upstream HTTP details: %T", stream.Err())
	}
	if got.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d", got.HTTPStatusCode())
	}
	if string(got.ResponseBody()) != `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit","param":null}}` {
		t.Fatalf("body = %s", got.ResponseBody())
	}
}

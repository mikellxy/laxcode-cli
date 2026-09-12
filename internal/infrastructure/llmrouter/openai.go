package llmrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// OpenAIStreamClient 使用服务端配置的凭据访问上游 Responses API。调用方请求
// 中的 model 会被 configuredModel 覆盖，避免网关使用者绕过本地模型配置。
type OpenAIStreamClient struct {
	client          openai.Client
	configuredModel string
}

var _ domainrouter.StreamClient = (*OpenAIStreamClient)(nil)

func NewOpenAIStreamClient(apiKey, baseURL, model string) *OpenAIStreamClient {
	return newOpenAIStreamClient(apiKey, baseURL, model)
}

func newOpenAIStreamClient(apiKey, baseURL, model string, extraOptions ...option.RequestOption) *OpenAIStreamClient {
	clientOptions := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		// 网关负责把一次请求的真实结果返回给调用方，不在 SDK 内静默重试，
		// 便于上层准确实施限流并观测每次上游调用。
		option.WithMaxRetries(0),
	}
	clientOptions = append(clientOptions, extraOptions...)
	return &OpenAIStreamClient{
		client:          openai.NewClient(clientOptions...),
		configuredModel: model,
	}
}

func (c *OpenAIStreamClient) GenerateStream(ctx context.Context, requestJSON []byte) (domainrouter.Stream, error) {
	// 保留客户端 body 的原始字段结构，只覆盖 model。不能先反序列化成
	// ResponseNewParams 再序列化：input/tool_choice 等 union 参数在这种
	// round-trip 下可能被 SDK 丢弃，第三方自定义字段也无法保留。
	var body map[string]json.RawMessage
	if err := json.Unmarshal(requestJSON, &body); err != nil || body == nil {
		if err == nil {
			err = errors.New("request body must be a JSON object")
		}
		return nil, &domainrouter.InvalidRequestError{
			Err: fmt.Errorf("decode OpenAI Responses request: %w", err),
		}
	}
	if c.configuredModel == "" {
		return nil, errors.New("configured OpenAI model is empty")
	}
	modelJSON, _ := json.Marshal(c.configuredModel)
	body["model"] = modelJSON
	upstreamBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI Responses request: %w", err)
	}

	// NewStreaming 在 WithRequestBody 之后自动设置 stream=true；空 params 仅用于
	// 满足 SDK 方法签名，真正发送的是保留了客户端字段的 upstreamBody。
	stream := c.client.Responses.NewStreaming(ctx, responses.ResponseNewParams{},
		option.WithRequestBody("application/json", upstreamBody))
	return &openAIStream{stream: stream}, nil
}

type openAIStream struct {
	stream *ssestream.Stream[responses.ResponseStreamEventUnion]
}

var _ domainrouter.Stream = (*openAIStream)(nil)

func (s *openAIStream) Next() bool {
	return s.stream.Next()
}

func (s *openAIStream) Current() domainrouter.StreamEvent {
	event := s.stream.Current()
	return domainrouter.StreamEvent{
		Type: event.Type,
		Data: []byte(event.RawJSON()),
	}
}

func (s *openAIStream) Err() error {
	err := s.stream.Err()
	if err == nil {
		return nil
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		body := []byte(apiErr.RawJSON())
		if json.Valid(body) {
			body, _ = json.Marshal(map[string]json.RawMessage{"error": body})
		}
		return &upstreamHTTPError{
			status: apiErr.StatusCode,
			body:   body,
			cause:  err,
		}
	}
	return err
}

func (s *openAIStream) Close() error {
	return s.stream.Close()
}

// upstreamHTTPError 通过窄接口向 HTTP adapter 暴露上游状态和原始错误体，
// application 无需依赖 OpenAI SDK 的具体错误类型。
type upstreamHTTPError struct {
	status int
	body   []byte
	cause  error
}

func (e *upstreamHTTPError) Error() string       { return e.cause.Error() }
func (e *upstreamHTTPError) Unwrap() error       { return e.cause }
func (e *upstreamHTTPError) HTTPStatusCode() int { return e.status }
func (e *upstreamHTTPError) ResponseBody() []byte {
	return e.body
}

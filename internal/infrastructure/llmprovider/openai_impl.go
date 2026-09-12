package llmprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	domainllm "github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type OpenApiProvider struct {
	client openai.Client
	model  string
	budget domainllm.ContextBudget

	streamGatewayURL string
	httpClient       *http.Client
}

// NewOpenApiProviderWithStreamGateway 构造一个仅将 GenerateStream 经本地网关
// 转发的 provider。Generate 与 CountInputTokens 仍使用上游 SDK client；这两条
// 路径分别服务上下文摘要和 token 计数，不属于主 ReAct 流式生成流量。
func NewOpenApiProviderWithStreamGateway(apiKey, baseURL, model, streamGatewayURL string, budgetValues ...int) *OpenApiProvider {
	p := NewOpenApiProvider(apiKey, baseURL, model, budgetValues...)
	p.streamGatewayURL = streamGatewayURL
	return p
}

// 编译期契约：基础设施 provider 必须满足领域层 LLMClient 接口。
var _ domainllm.LLMClient = (*OpenApiProvider)(nil)

func NewOpenApiProvider(apiKey, baseURL, model string, budgetValues ...int) *OpenApiProvider {
	// 可变参只用于保持库内旧调用方的源码兼容；组合根始终传入
	// 由配置解析得到的窗口和输出预留。
	contextWindow, reservedOutput := 128_000, 16_384
	if len(budgetValues) >= 2 {
		contextWindow, reservedOutput = budgetValues[0], budgetValues[1]
	}
	return &OpenApiProvider{
		client:     openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:      model,
		httpClient: http.DefaultClient,
		budget: domainllm.ContextBudget{
			ContextWindow:        contextWindow,
			ReservedOutputTokens: reservedOutput,
		},
	}
}

func (p *OpenApiProvider) ContextBudget() domainllm.ContextBudget {
	return p.budget
}

// CountInputTokens 调用 Responses 的 input-token counting 端点。这里先复用
// buildResponseParams，再把其输入项与工具原样放入计数请求，保证计数
// 与真正 Generate 的结构口径一致。
//
// 远端计数端点并非所有兼容实现都提供（例如 DeepSeek 未实现
// /responses/input_tokens，返回 404）。任一远端失败都退回本地 tiktoken
// 估算，避免因缺少计数端点而中断整个 ReAct 循环；仅当本地估算也失败时
// 才向调用方暴露原始远端错误。
func (p *OpenApiProvider) CountInputTokens(ctx context.Context, msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) (int, error) {
	resp, err := p.client.Responses.InputTokens.Count(ctx, p.buildInputTokenCountParams(msgs, toolsDefs))
	if err != nil {
		if local, localErr := p.countInputTokensLocal(msgs, toolsDefs); localErr == nil {
			return local, nil
		}
		return 0, fmt.Errorf("count response input tokens: %w", err)
	}
	return int(resp.InputTokens), nil
}

func (p *OpenApiProvider) buildInputTokenCountParams(msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) responses.InputTokenCountParams {
	params := p.buildResponseParams(msgs, toolsDefs)
	return responses.InputTokenCountParams{
		Model: openai.String(p.model),
		Input: responses.InputTokenCountParamsInputUnion{
			OfResponseInputItemArray: params.Input.OfInputItemList,
		},
		Tools: params.Tools,
	}
}

func (p *OpenApiProvider) Generate(ctx context.Context, msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	reqParams := p.buildResponseParams(msgs, toolsDefs)

	resp, err := p.client.Responses.New(ctx, reqParams)
	if err != nil {
		return nil, err
	}

	msg := &sharedkernel.Message{
		Role:    sharedkernel.RoleAssistant,
		Content: resp.OutputText(),
		TokenUsed: sharedkernel.TokenStatistics{
			TokenInput:  int(resp.Usage.InputTokens),
			TokenOutput: int(resp.Usage.OutputTokens),
		},
	}
	for _, output := range resp.Output {
		switch output.Type {
		case "reasoning":
			r := output.AsReasoning()
			msg.ReasoningID = r.ID
			for _, c := range r.Content {
				msg.ReasoningContent += c.Text
			}
		case "function_call":
			c := output.AsFunctionCall()
			msg.ToolCalls = append(msg.ToolCalls, sharedkernel.ToolCall{
				ID:        c.CallID,
				Name:      c.Name,
				Arguments: json.RawMessage(c.Arguments),
			})
		}
	}

	return msg, nil
}

// buildResponseParams 把会话消息与工具定义组装为 Responses API 请求参数，
// 供批式 Generate 与流式 GenerateStream 共用，保证两条路径的输入口径一致。
func (p *OpenApiProvider) buildResponseParams(msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) responses.ResponseNewParams {
	var inputParams responses.ResponseNewParamsInputUnion

	for _, msg := range msgs {
		switch msg.Role {
		case sharedkernel.RoleSystem:
			item := responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem)
			inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
		case sharedkernel.RoleUser:
			item := responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleUser)
			inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
		case sharedkernel.RoleTool:
			item := responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, msg.Content)
			inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
		case sharedkernel.RoleAssistant:
			// The reasoning item must precede the message and function_call
			// items of the same turn: it is the thinking part of that output.
			if msg.ReasoningContent != "" {
				inputParams.OfInputItemList = append(inputParams.OfInputItemList, responses.ResponseInputItemUnionParam{
					OfReasoning: &responses.ResponseReasoningItemParam{
						ID: msg.ReasoningID,
						Content: []responses.ResponseReasoningItemContentParam{
							{Text: msg.ReasoningContent},
						},
					},
				})
			}
			if len(msg.Content) > 0 {
				item := responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant)
				inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					item := responses.ResponseInputItemParamOfFunctionCall(string(tc.Arguments), tc.ID, tc.Name)
					inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
				}
			}
		}

	}

	reqParams := responses.ResponseNewParams{
		Model: p.model,
		Input: inputParams,
	}
	if p.budget.ReservedOutputTokens > 0 {
		reqParams.MaxOutputTokens = openai.Int(int64(p.budget.ReservedOutputTokens))
	}

	if len(toolsDefs) > 0 {
		for _, td := range toolsDefs {
			tool := responses.ToolParamOfFunction(td.Name, td.Parameters, true)
			tool.OfFunction.Description = openai.String(td.Description)
			reqParams.Tools = append(reqParams.Tools, tool)
		}
	}

	return reqParams
}

// GenerateStream 是批式 Generate 的流式对应：通过本地路由器取得 Responses
// SSE 事件，一边经 emit 实时推送领域级增量（正文 / reasoning 三段式、完整
// 工具调用），一边累积出与批式 Generate 语义等价的完整消息返回。事件分派
// 见 design 决策 5 的映射表。工具调用不流式：以 output_item.done 的完整 item
// 为权威源取参数（与批式读 resp.Output 同源），故 function_call_arguments.delta
// 不需处理；token 用量只在 response.completed 可得。
func (p *OpenApiProvider) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition,
	emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	reqParams := p.buildResponseParams(msgs, toolsDefs)

	stream, err := p.newResponseStream(ctx, reqParams)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	msg := &sharedkernel.Message{Role: sharedkernel.RoleAssistant}
	// 三段式边界：首个 delta 惰性触发 start，对应 done 事件触发 end
	var textStarted, reasoningStarted bool

	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			delta := ev.AsResponseOutputTextDelta().Delta
			if !textStarted {
				emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextStart})
				textStarted = true
			}
			msg.Content += delta
			emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta, Delta: delta})
		case "response.output_text.done":
			if textStarted {
				emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextEnd})
				textStarted = false
			}
		case "response.reasoning_text.delta":
			delta := ev.AsResponseReasoningTextDelta().Delta
			if !reasoningStarted {
				emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningStart})
				reasoningStarted = true
			}
			emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningDelta, Delta: delta})
		case "response.reasoning_summary_text.delta":
			delta := ev.AsResponseReasoningSummaryTextDelta().Delta
			if !reasoningStarted {
				emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningStart})
				reasoningStarted = true
			}
			emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningDelta, Delta: delta})
		case "response.output_item.done":
			item := ev.AsResponseOutputItemDone().Item
			switch item.Type {
			case "reasoning":
				r := item.AsReasoning()
				msg.ReasoningID = r.ID
				for _, c := range r.Content {
					msg.ReasoningContent += c.Text
				}
				if reasoningStarted {
					emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningEnd})
					reasoningStarted = false
				}
			case "function_call":
				c := item.AsFunctionCall()
				tc := sharedkernel.ToolCall{
					ID:        c.CallID,
					Name:      c.Name,
					Arguments: json.RawMessage(c.Arguments),
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
				emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkToolCall, ToolCall: &tc})
			}
		case "response.completed":
			resp := ev.AsResponseCompleted().Response
			msg.TokenUsed = sharedkernel.TokenStatistics{
				TokenInput:  int(resp.Usage.InputTokens),
				TokenOutput: int(resp.Usage.OutputTokens),
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	return msg, nil
}

// newResponseStream 在正常程序装配下把请求体 POST 到进程内本地路由器；构造函数
// 未提供网关 URL 时保留 SDK 直连，供独立 provider 使用及向后兼容现有调用方。
func (p *OpenApiProvider) newResponseStream(ctx context.Context, params responses.ResponseNewParams) (*ssestream.Stream[responses.ResponseStreamEventUnion], error) {
	if p.streamGatewayURL == "" {
		return p.client.Responses.NewStreaming(ctx, params), nil
	}

	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal local LLM router request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.streamGatewayURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create local LLM router request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	httpClient := p.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call local LLM router: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("local LLM router returned %s: %s", resp.Status, bytes.TrimSpace(errorBody))
	}

	return ssestream.NewStream[responses.ResponseStreamEventUnion](ssestream.NewDecoder(resp), nil), nil
}

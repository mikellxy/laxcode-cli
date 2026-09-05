package llmprovider

import (
	"context"
	"encoding/json"

	"github.com/mikellxy/laxcode/internal/config"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

type OpenApiProvider struct {
	client openai.Client
	model  string
}

func NewOpenApiProvider(apiKey, baseURL, model string) *OpenApiProvider {
	return &OpenApiProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
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
				config.Debugf("reasoning replayed: id=%q len=%d", msg.ReasoningID, len(msg.ReasoningContent))
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

	if len(toolsDefs) > 0 {
		for _, td := range toolsDefs {
			tool := responses.ToolParamOfFunction(td.Name, td.Parameters, true)
			tool.OfFunction.Description = openai.String(td.Description)
			reqParams.Tools = append(reqParams.Tools, tool)
		}
	}

	return reqParams
}

// GenerateStream 是批式 Generate 的流式对应：用 Responses.NewStreaming 消费
// SSE 事件，一边经 emit 实时推送领域级增量（正文 / reasoning 三段式、完整
// 工具调用），一边累积出与批式 Generate 语义等价的完整消息返回。事件分派
// 见 design 决策 5 的映射表。工具调用不流式：以 output_item.done 的完整 item
// 为权威源取参数（与批式读 resp.Output 同源），故 function_call_arguments.delta
// 不需处理；token 用量只在 response.completed 可得。
func (p *OpenApiProvider) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition,
	emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	reqParams := p.buildResponseParams(msgs, toolsDefs)

	stream := p.client.Responses.NewStreaming(ctx, reqParams)
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
				config.Debugf("reasoning streamed: id=%q len=%d", r.ID, len(msg.ReasoningContent))
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

package provider

import (
	"context"
	"encoding/json"

	"github.com/mikellxy/laxcode/internal/config"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// 包级声明保证命令行参数在 main 的 config.Parse() 之前完成注册；
// 三个配置项均支持 命令行参数 > 环境变量 > ~/.laxcode/settings.json
var (
	apiKey  = config.String(config.Item{Flag: "OPENAI_API_KEY", Env: "OPENAI_API_KEY", Key: "OPENAI_API_KEY", Usage: "OpenAI API key"})
	baseURL = config.String(config.Item{Flag: "OPENAI_BASE_URL", Env: "OPENAI_BASE_URL", Key: "OPENAI_BASE_URL", Usage: "OpenAI base URL"})
	model   = config.String(config.Item{Flag: "OPENAI_MODEL", Env: "OPENAI_MODEL", Key: "OPENAI_MODEL", Usage: "OpenAI model"})
)

type OpenApiProvider struct {
	client openai.Client
	model  string
	info   Info
}

func NewOpenApiProvider(info Info) *OpenApiProvider {
	// Name 缺省用实际使用的模型名，日志里看到的永远是真实模型
	if info.Name == "" {
		info.Name = model.Get()
	}
	return &OpenApiProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey.Get()), option.WithBaseURL(baseURL.Get())),
		model:  model.Get(),
		info:   info,
	}
}

func (p *OpenApiProvider) Info() *Info {
	return &p.info
}

func (p *OpenApiProvider) Generate(ctx context.Context, msgs []schema.Message, toolsDefs []schema.ToolDefinition) (*schema.Message, error) {
	reqParams := p.buildResponseParams(msgs, toolsDefs)

	resp, err := p.client.Responses.New(ctx, reqParams)
	if err != nil {
		return nil, err
	}

	msg := &schema.Message{
		Role:    schema.RoleAssistant,
		Content: resp.OutputText(),
		TokenUsed: schema.TokenStatistics{
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
			config.Debugf("reasoning parsed: id=%q len=%d", r.ID, len(msg.ReasoningContent))
		case "function_call":
			c := output.AsFunctionCall()
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
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
func (p *OpenApiProvider) buildResponseParams(msgs []schema.Message, toolsDefs []schema.ToolDefinition) responses.ResponseNewParams {
	var inputParams responses.ResponseNewParamsInputUnion

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			item := responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem)
			inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
		case schema.RoleUser:
			if msg.ToolCallID != "" {
				item := responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, msg.Content)
				inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
			} else {
				item := responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleUser)
				inputParams.OfInputItemList = append(inputParams.OfInputItemList, item)
			}

		case schema.RoleAssistant:
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
func (p *OpenApiProvider) GenerateStream(ctx context.Context, msgs []schema.Message, toolsDefs []schema.ToolDefinition, emit func(StreamChunk)) (*schema.Message, error) {
	reqParams := p.buildResponseParams(msgs, toolsDefs)

	stream := p.client.Responses.NewStreaming(ctx, reqParams)
	defer stream.Close()

	msg := &schema.Message{Role: schema.RoleAssistant}
	// 三段式边界：首个 delta 惰性触发 start，对应 done 事件触发 end
	var textStarted, reasoningStarted bool

	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			delta := ev.AsResponseOutputTextDelta().Delta
			if !textStarted {
				emit(StreamChunk{Kind: ChunkTextStart})
				textStarted = true
			}
			msg.Content += delta
			emit(StreamChunk{Kind: ChunkTextDelta, Delta: delta})
		case "response.output_text.done":
			if textStarted {
				emit(StreamChunk{Kind: ChunkTextEnd})
				textStarted = false
			}
		case "response.reasoning_text.delta":
			delta := ev.AsResponseReasoningTextDelta().Delta
			if !reasoningStarted {
				emit(StreamChunk{Kind: ChunkReasoningStart})
				reasoningStarted = true
			}
			emit(StreamChunk{Kind: ChunkReasoningDelta, Delta: delta})
		case "response.reasoning_summary_text.delta":
			delta := ev.AsResponseReasoningSummaryTextDelta().Delta
			if !reasoningStarted {
				emit(StreamChunk{Kind: ChunkReasoningStart})
				reasoningStarted = true
			}
			emit(StreamChunk{Kind: ChunkReasoningDelta, Delta: delta})
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
					emit(StreamChunk{Kind: ChunkReasoningEnd})
					reasoningStarted = false
				}
				config.Debugf("reasoning streamed: id=%q len=%d", r.ID, len(msg.ReasoningContent))
			case "function_call":
				c := item.AsFunctionCall()
				tc := schema.ToolCall{
					ID:        c.CallID,
					Name:      c.Name,
					Arguments: json.RawMessage(c.Arguments),
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
				emit(StreamChunk{Kind: ChunkToolCall, ToolCall: &tc})
			}
		case "response.completed":
			resp := ev.AsResponseCompleted().Response
			msg.TokenUsed = schema.TokenStatistics{
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

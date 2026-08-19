package provider

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type OpenApiProvider struct {
	client openai.Client
	model  string
	info   Info
}

func NewOpenApiProvider(info Info) *OpenApiProvider {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseUrl := os.Getenv("OPENAI_BASE_URL")
	model := os.Getenv("OPENAI_MODEL")

	return &OpenApiProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl)),
		model:  model,
		info:   info,
	}
}

func (p *OpenApiProvider) Info() *Info {
	return &p.info
}

func (p *OpenApiProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	var inputMsgs []openai.ChatCompletionMessageParamUnion

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			inputMsgs = append(inputMsgs, openai.SystemMessage(msg.Content))

		case schema.RoleUser:
			if msg.ToolCallID != "" {
				inputMsgs = append(inputMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			} else {
				inputMsgs = append(inputMsgs, openai.UserMessage(msg.Content))
			}

		case schema.RoleAssistant:
			var astMsgParam openai.ChatCompletionAssistantMessageParam
			if len(msg.Content) > 0 {
				astMsgParam.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}

			if len(msg.ToolCalls) > 0 {
				var toolCallParams []openai.ChatCompletionMessageToolCallUnionParam
				for _, tc := range msg.ToolCalls {
					toolCallParams = append(toolCallParams, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(tc.Arguments),
							},
						},
					})
				}
				astMsgParam.ToolCalls = toolCallParams
			}

			inputMsgs = append(inputMsgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &astMsgParam,
			})

		}

	}

	params := openai.ChatCompletionNewParams{
		Messages: inputMsgs,
		Model:    p.model,
	}

	if len(tools) > 0 {
		var toolParams []openai.ChatCompletionToolUnionParam
		for _, tool := range tools {
			toolParams = append(toolParams, openai.ChatCompletionToolUnionParam{
				OfFunction: &openai.ChatCompletionFunctionToolParam{
					Function: openai.FunctionDefinitionParam{
						Name:        tool.Name,
						Description: openai.String(tool.Description),
						Parameters:  openai.FunctionParameters(tool.Parameters),
					},
				},
			})
		}
		params.Tools = toolParams
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}

	choice := resp.Choices[0].Message

	msg := &schema.Message{
		Role:    schema.RoleAssistant,
		Content: choice.Content,
	}

	if len(choice.ToolCalls) > 0 {
		for _, tc := range choice.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			})
		}
	}

	return msg, nil
}

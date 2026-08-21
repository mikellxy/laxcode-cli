package provider

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
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

func (p *OpenApiProvider) Generate(ctx context.Context, msgs []schema.Message, toolsDefs []schema.ToolDefinition) ([]schema.Message, error) {
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

	resp, err := p.client.Responses.New(ctx, reqParams)
	if err != nil {
		return nil, err
	}
	var respMsgs []schema.Message
	//for _, output := range resp.Output {
	//	switch output.Type {
	//	case "function_call":
	//		c := output.AsFunctionCall()
	//		msg := schema.Message{
	//			Role: schema.RoleAssistant,
	//		}
	//		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
	//			ID:        c.CallID,
	//			Name:      c.Name,
	//			Arguments: json.RawMessage(c.Arguments),
	//		})
	//		respMsgs = append(respMsgs, msg)
	//	case "message":
	//		c := output.AsMessage()
	//		for _, content := range c.Content {
	//			msg := schema.Message{
	//				Role:    schema.Role(c.Role),
	//				Content: content.Text,
	//			}
	//			respMsgs = append(respMsgs, msg)
	//		}
	//	}
	//}

	msg := schema.Message{
		Role:    schema.RoleAssistant,
		Content: resp.OutputText(),
	}
	for _, output := range resp.Output {
		switch output.Type {
		case "function_call":
			c := output.AsFunctionCall()
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:        c.CallID,
				Name:      c.Name,
				Arguments: json.RawMessage(c.Arguments),
			})
		}
	}
	respMsgs = append(respMsgs, msg)

	return respMsgs, nil
}

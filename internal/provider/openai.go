package provider

import (
	"context"
	"os"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type OpenApiProvider struct {
	client openai.Client
	model  string
}

func NewOpenApiProvider() *OpenApiProvider {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseUrl := os.Getenv("OPENAI_BASE_URL")
	model := os.Getenv("OPENAI_MODEL")

	return &OpenApiProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl)),
		model:  model,
	}
}

func (p *OpenApiProvider) Generate(ctx context.Context, msgs []schema.Message) (*schema.Message, error) {
	var inputMsgs []openai.ChatCompletionMessageParamUnion

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			inputMsgs = append(inputMsgs, openai.SystemMessage(msg.Content))

		case schema.RoleUser:
			inputMsgs = append(inputMsgs, openai.UserMessage(msg.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Messages: inputMsgs,
		Model:    p.model,
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}

	choice := resp.Choices[0].Message

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: choice.Content,
	}, nil
}

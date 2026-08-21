package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/mikellxy/laxcode/internal/schema"
)

type AnthropicProvider struct {
	client anthropic.Client
	model  string
	info   Info
}

func NewAnthropicProvider(info Info) *AnthropicProvider {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseUrl := os.Getenv("ANTHROPIC_BASE_URL")
	model := os.Getenv("ANTHROPIC_MODEL")

	return &AnthropicProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl)),
		model:  model,
		info:   info,
	}
}

func (p *AnthropicProvider) Info() *Info {
	return &p.info
}

func (p *AnthropicProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) ([]schema.Message, error) {
	var inputMsgs []anthropic.MessageParam
	var sysPrompt string

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			sysPrompt = msg.Content
		case schema.RoleUser:
			if len(msg.ToolCallID) != 0 {
				inputMsgs = append(inputMsgs, anthropic.NewUserMessage(
					anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false),
				))
			} else {
				inputMsgs = append(inputMsgs, anthropic.NewUserMessage(
					anthropic.NewTextBlock(msg.Content),
				))
			}
		case schema.RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if len(msg.Content) > 0 {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					blocks = append(blocks, anthropic.ContentBlockParamUnion{
						OfToolUse: &anthropic.ToolUseBlockParam{
							ID:    tc.ID,
							Name:  tc.Name,
							Input: tc.Arguments,
						},
					})
				}
			}
			if len(blocks) > 0 {
				inputMsgs = append(inputMsgs, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}

	var toolParams []anthropic.ToolUnionParam
	for _, tool := range tools {
		rq, ok := tool.Parameters[schema.ToolDefParamRequired].([]string)
		if !ok {
			rq = []string{}
		}
		toolParams = append(toolParams, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: tool.Parameters[schema.ToolDefParamProperties],
					Required:   rq,
				},
			},
		})
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		Messages:  inputMsgs,
		MaxTokens: 4096,
	}
	if len(sysPrompt) > 0 {
		params.System = []anthropic.TextBlockParam{
			{Text: sysPrompt},
		}
	}
	if len(toolParams) > 0 {
		params.Tools = toolParams
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("Claude/Zhipu API 请求失败: %w", err)
	}

	msg := schema.Message{
		Role: schema.RoleAssistant,
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			inputRaw, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: inputRaw,
			})
		}
	}

	return []schema.Message{msg}, nil
}

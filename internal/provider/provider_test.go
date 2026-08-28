package provider

import (
	"context"
	"os"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

func TestOpenAIProvider_GenerateWithTool(t *testing.T) {
	// 集成测试：真实调用 LLM API，需本地配置 API 凭证后通过
	// LAXCODE_INTEGRATION=1 显式开启，默认跳过（如 CI 环境）
	if os.Getenv("LAXCODE_INTEGRATION") == "" {
		t.Skip("skipping integration test; set LAXCODE_INTEGRATION=1 to run")
	}
	t.Parallel()
	testSet := []struct {
		name         string
		msgs         []schema.Message
		toolRegistry tools.Registry
		providers    []Provider
	}{
		{
			name: "user ask about the content of file",
			msgs: []schema.Message{
				{Role: schema.RoleUser, Content: "internal/tools/read_file.go文件实现了什么功能"},
			},
			toolRegistry: tools.NewDefaultRegistry(nil, nil),
			providers: []Provider{
				//NewOpenApiProvider(Info{Name: "deepseek anthropic"}),
				NewAnthropicProvider(Info{Name: "deepseek openai"}),
			},
		},
	}

	for _, test := range testSet {
		for _, p := range test.providers {
			t.Run(test.name, func(t *testing.T) {
				msgs := test.msgs
				respMsg, err := p.Generate(context.Background(), msgs, test.toolRegistry.GetAvailableTools())
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
				msgs = append(msgs, *respMsg)

				var toolCalls []schema.ToolCall
				if respMsg.Role != schema.RoleAssistant {
					t.Errorf("Generate() role = %q, want %q", respMsg.Role, schema.RoleAssistant)
				}
				toolCalls = append(toolCalls, respMsg.ToolCalls...)
				if len(toolCalls) == 0 {
					t.Errorf("Generate() tool calls = %v, want at least one tool call", toolCalls)
				}
				for _, tc := range toolCalls {
					toolResult := test.toolRegistry.Execute(context.Background(), &tc)
					msgs = append(msgs, schema.Message{
						Role:       schema.RoleUser,
						Content:    toolResult.Output,
						ToolCallID: toolResult.ToolCallID,
					})
				}

				respMsg, err = p.Generate(context.Background(), msgs, test.toolRegistry.GetAvailableTools())
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
				if respMsg.Role != schema.RoleAssistant {
					t.Errorf("Generate() role = %q, want %q", respMsg.Role, schema.RoleAssistant)
				}
				t.Logf("[%v] Generate() response = %q\n", p.Info(), respMsg.Content)
			})
		}
	}
}

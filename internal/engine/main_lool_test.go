package engine

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

func TestMianLoop_GenerateWithTool(t *testing.T) {
	t.Parallel()
	testSet := []struct {
		name         string
		msgs         []schema.Message
		toolRegistry tools.Registry
		engines      []*AgentEngine
	}{
		{
			name:         "user ask the weather of beijing",
			toolRegistry: tools.NewDefaultRegistry(),
			engines: []*AgentEngine{
				NewAgentEngine(tools.NewDefaultRegistry(), provider.NewOpenApiProvider(provider.Info{Name: "deepseek openai"})),
				NewAgentEngine(tools.NewDefaultRegistry(), provider.NewAnthropicProvider(provider.Info{Name: "deepseek anthropic"})),
			},
		},
	}

	for _, test := range testSet {
		for _, e := range test.engines {
			t.Run(test.name, func(t *testing.T) {
				msg, err := e.Run(context.Background(), "main_loop.go文件中实现了什么功能")
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("[%v] generate: %s", e.Provider.Info(), msg.Content)
			})
		}
	}
}

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
			name: "user ask the weather of beijing",
			msgs: []schema.Message{
				{Role: schema.RoleUser, Content: "北京现在的天气是否适合户外跑步?"},
			},
			toolRegistry: tools.NewFakeRegistry(),
			engines: []*AgentEngine{
				NewAgentEngine(tools.NewFakeRegistry(), provider.NewOpenApiProvider(provider.Info{Name: "deepseek openai"})),
				NewAgentEngine(tools.NewFakeRegistry(), provider.NewAnthropicProvider(provider.Info{Name: "deepseek anthropic"})),
			},
		},
	}

	for _, test := range testSet {
		for _, e := range test.engines {
			t.Run(test.name, func(t *testing.T) {
				msg, err := e.Loop(context.Background(), "北京现在的天气是否适合户外跑步?")
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("[%v] generate: %s", e.Provider.Info(), msg.Content)
			})
		}
	}
}

package engine

import (
	"context"
	"errors"

	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

type AgentEngine struct {
	ToolRegistry tools.Registry
	Provider     provider.Provider
}

func NewAgentEngine(toolRegistry tools.Registry, provider provider.Provider) *AgentEngine {
	return &AgentEngine{
		ToolRegistry: toolRegistry,
		Provider:     provider,
	}
}

func (f *AgentEngine) Loop(ctx context.Context, prompt string) (*schema.Message, error) {
	var contextHis []schema.Message
	contextHis = append(contextHis, schema.Message{
		Role:    schema.RoleSystem,
		Content: "你是一个全能AI助理",
	})
	contextHis = append(contextHis, schema.Message{
		Role:    schema.RoleUser,
		Content: prompt,
	})

	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 10 {
			return nil, errors.New("too many turns")
		}
		msg, err := f.Provider.Generate(ctx, contextHis, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return nil, err
		}
		if len(msg.ToolCalls) == 0 {
			return msg, nil
		}

		contextHis = append(contextHis, *msg)
		for _, toolCall := range msg.ToolCalls {
			toolResult := f.ToolRegistry.Execute(ctx, &toolCall)
			contextHis = append(contextHis, schema.Message{
				Role:       schema.RoleUser,
				Content:    toolResult.Output,
				ToolCallID: toolResult.ToolCallID,
			})
		}
	}
}

package engine

import (
	"context"
	"testing"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

func TestMianLoop_GenerateWithTool(t *testing.T) {
	t.Parallel()
	testSet := []struct {
		name         string
		toolRegistry tools.Registry
		engines      []*AgentEngine
	}{
		{
			name:         "user ask the weather of beijing",
			toolRegistry: tools.NewDefaultRegistry(),
			engines: []*AgentEngine{
				NewAgentEngine(tools.NewDefaultRegistry(), provider.NewOpenApiProvider(provider.Info{Name: "deepseek openai"}), "."),
				NewAgentEngine(tools.NewDefaultRegistry(), provider.NewAnthropicProvider(provider.Info{Name: "deepseek anthropic"}), "."),
			},
		},
	}

	for _, test := range testSet {
		for _, e := range test.engines {
			t.Run(test.name, func(t *testing.T) {
				// 模拟 Loop 的会话历史：system prompt 由 Run 内 View 组装注入（不落盘），
				// 这里只经 Append 注入用户问题
				sess := newSession(t.TempDir(), "test-session")
				sess.Append(schema.Message{Role: schema.RoleUser, Content: "main_loop.go文件中实现了什么功能"})

				sysPrompt := laxctx.BuildSysPrompt(e.WorkingDir, laxctx.LoadSkills(e.WorkingDir), false, "test-session")
				sess.View(sysPrompt)
				if err := e.Run(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
				t.Logf("[%v] generate done", e.Provider.Info())
			})
		}
	}
}

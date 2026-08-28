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
	providers := []provider.Provider{
		provider.NewOpenApiProvider(provider.Info{Name: "deepseek openai"}),
		provider.NewAnthropicProvider(provider.Info{Name: "deepseek anthropic"}),
	}

	for _, p := range providers {
		t.Run("user ask the weather of beijing", func(t *testing.T) {
			// 模拟 main 的装配顺序：先构造会话（注入 system prompt 与用户
			// 问题），再以会话构造引擎并运行
			sess := newSession(t.TempDir(), "test-session")
			sess.Append(schema.Message{Role: schema.RoleUser, Content: "main_loop.go文件中实现了什么功能"})
			sess.View(laxctx.BuildSysPrompt(".", laxctx.LoadSkills("."), false, "test-session"))

			e := NewAgentEngine(tools.NewDefaultRegistry(nil), p, ".", false, sess)
			if _, err := e.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Logf("[%v] generate done", e.Provider.Info())
		})
	}
}

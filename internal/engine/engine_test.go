package engine

import (
	"context"
	"os"
	"testing"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

func TestMianLoop_GenerateWithTool(t *testing.T) {
	// 集成测试：真实调用 LLM API，需本地配置 API 凭证后通过
	// LAXCODE_INTEGRATION=1 显式开启，默认跳过（如 CI 环境）
	if os.Getenv("LAXCODE_INTEGRATION") == "" {
		t.Skip("skipping integration test; set LAXCODE_INTEGRATION=1 to run")
	}
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

			e := NewAgentEngine(tools.NewDefaultRegistry(nil, nil), p, ".", false, sess, nil)
			if _, err := e.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Logf("[%v] generate done", e.Provider.Info())
		})
	}
}

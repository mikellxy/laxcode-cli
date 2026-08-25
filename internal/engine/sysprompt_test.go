package engine

import (
	"strings"
	"testing"

	laxctx "github.com/mikellxy/laxcode/internal/context"
)

func TestBuildSysPrompt(t *testing.T) {
	t.Parallel()

	t.Run("零技能时不含技能索引段", func(t *testing.T) {
		got := laxctx.BuildSysPrompt("/w", nil)
		if !strings.Contains(got, "工作目录: /w") {
			t.Errorf("应包含静态基础模板段: %q", got)
		}
		if strings.Contains(got, "可用技能") {
			t.Errorf("零技能时不应包含技能索引段: %q", got)
		}
	})

	t.Run("有技能时在静态模板后追加索引段", func(t *testing.T) {
		got := laxctx.BuildSysPrompt("/w", []laxctx.Skill{{Name: "example", Description: "示例技能"}})
		if !strings.Contains(got, "工作目录: /w") {
			t.Errorf("应包含静态基础模板段: %q", got)
		}
		if !strings.Contains(got, "## 可用技能（Skills）") ||
			!strings.Contains(got, ".laxcode/skills/<技能名>/SKILL.md") ||
			!strings.Contains(got, "- example: 示例技能") {
			t.Errorf("应包含技能索引段（标题/路径规则/条目）: %q", got)
		}
		if !strings.Contains(got, "输出技术方案\n\n## 可用技能") {
			t.Errorf("索引段应以空行衔接在静态模板之后: %q", got)
		}
	})
}

package prompt

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
)

//go:embed tmpl/personality.md
var personalityPrompt string

//go:embed tmpl/plan_mode.md
var planModePrompt string

// GetSysPrompt 返回 agent 的 system prompt：人格提示词（含工作目录沙箱约束）
// 拼接工作目录下的技能索引段；planMode 为真时再追加 Plan Mode 工作流提示词。
// 人格与 plan 模板均以 %s 占位，分别填入 workDir 与会话目录
// ${workDir}/.laxcode/.session/${sessID}（Plan Mode 规划文件的落盘位置）。
func GetSysPrompt(workDir, sessID string, planMode bool) string {
	var sb strings.Builder

	// 人格提示词含 %s 工作目录占位，须格式化填入（沙箱约束依赖它）
	sb.WriteString(fmt.Sprintf(personalityPrompt, workDir))

	if index := RenderSkillIndex(LoadSkills(workDir)); index != "" {
		sb.WriteString("\n\n")
		sb.WriteString(index)
	}

	if planMode {
		sessDir := filepath.Join(workDir, ".laxcode", ".session", sessID)
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf(planModePrompt, sessDir))
	}

	return sb.String()
}

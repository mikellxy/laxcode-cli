package prompt

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed tmpl/personality.md
var personalityPrompt string

//go:embed tmpl/plan_mode.md
var planModePrompt string

// PlanMode 是 Plan Mode 段的渲染入参。SessionDir 是本次会话规划文件
// （plan.md / design.md 等）的落盘目录，由组合根按磁盘布局算好后注入
// （见 infrastructure/layout.SessionDir），领域层不再自行拼路径。
type PlanMode struct {
	SessionDir string
}

// GetSysPrompt 返回 agent 的 system prompt：通用工程提示词（含工作区边界）
// 拼接已加载的技能索引段；plan 非 nil 时再追加 Plan Mode 工作流提示词。
//
// skills 由调用方经 LoadSkills 预先加载（技能发现属 SkillSource 端口职责，
// 且按规范在会话启动时快照一次），传 nil 即无技能；plan 传 nil 表示不启用
// Plan Mode，因而无需为本用不上的会话目录编造取值（子 Agent 即此场景）。
// 人格与 plan 模板均以 %s 占位，分别填入 workDir 与 plan.SessionDir。
func GetSysPrompt(workDir string, skills []Skill, plan *PlanMode) string {
	var sb strings.Builder

	// 通用工程提示词含 %s 工作目录占位，须格式化填入（工作区边界依赖它）
	sb.WriteString(fmt.Sprintf(personalityPrompt, workDir))

	if index := RenderSkillIndex(skills); index != "" {
		sb.WriteString("\n\n")
		sb.WriteString(index)
	}

	if plan != nil {
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf(planModePrompt, plan.SessionDir))
	}

	return sb.String()
}

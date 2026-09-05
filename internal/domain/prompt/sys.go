package prompt

import (
	_ "embed"
)

//go:embed tmpl/personality.md
var personalityPrompt string

// GetSysPrompt 返回 agent 的 system prompt：人格提示词拼接工作目录下的
// 技能索引段；无有效技能时只返回人格提示词。
func GetSysPrompt(workDir string) string {
	skillIndex := RenderSkillIndex(LoadSkills(workDir))
	if skillIndex == "" {
		return personalityPrompt
	}
	return personalityPrompt + "\n\n" + skillIndex
}

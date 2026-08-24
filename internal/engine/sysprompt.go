package engine

import (
	"fmt"

	laxctx "github.com/mikellxy/laxcode/internal/context"
)

var sysPrompt string = `你是一个智能AI助理，你可以处理的文件位于工作目录: %s
进行编程类任务时，如果无法确定文件，可以先试用 bash 工具，执行grep命令搜索目标代码在什么文件文件，然后使用 read 工具阅读代码，输出技术方案
`

// BuildSysPrompt 组装 system prompt：静态基础模板 + 技能索引段。
// 技能索引段由 context 包渲染，零技能时返回空串、整段省略。
func BuildSysPrompt(workDir string, skills []laxctx.Skill) string {
	prompt := fmt.Sprintf(sysPrompt, workDir)
	if index := laxctx.RenderSkillIndex(skills); index != "" {
		return prompt + "\n" + index
	}
	return prompt
}

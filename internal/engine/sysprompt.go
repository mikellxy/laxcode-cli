package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/env"
)

// personalityPrompt is the base system prompt template for the AI agent.
// English translation: "You are a general-purpose AI agent capable of completing
// various complex tasks submitted by users (e.g., solution design, code development)
// based on your own reasoning and appropriate tool usage. [Sandbox Mandatory
// Constraints] All file read/create/modify operations can only be performed within
// the working directory: %s. Reading or writing files outside this directory is
// prohibited; accessing system-sensitive files is prohibited. [Tool Usage Iron
// Rules, Must Be Strictly Followed] 1. To list folders, browse directory structures,
// and search files, you may ONLY use the bash tool to run commands such as ls, find,
// grep. Never pass a folder path to read_file; read_file is used only to read a
// single specific file's content. [Output Rules] Thought content is only for internal
// decision-making; final conclusions should be clear and include a summary of changes.
// Do not proactively fabricate non-existent files, directories, or code content."
var personalityPrompt string = `你是一个通用AI智能体，能够基于自身推理和恰当的工具使用完成用户提交的各类复杂任务，如方案设计、代码开发等。  
【沙箱强制约束】  
所有文件读取、新建、修改操作，只能在工作目录：%s 内部执行。  
禁止读写该目录以外任何路径的文件；禁止访问系统敏感文件。  

【工具使用铁则，必须严格遵守】  
1. 列出文件夹、浏览目录结构、搜索文件，**只能使用 bash 工具执行 ls, find, grep 等命令**。永远不要把文件夹路径传给 read_file；read_file仅用于读取单个具体文件内容。

【输出规则】  
思考内容仅用于内部决策；最终结论清晰、给出改动总结。  
不需要主动编造不存在的文件、目录、代码内容。`

// planModePrompt is the system prompt template used when the user enables Plan-Mode.
// English translation: "The user has enabled Plan-Mode. All planning files are saved
// to: %s. The execution order must not be skipped: Step1: Generate plan.md, containing
// the goal, requirements, and the implementation approach for each point. Step2: From
// plan.md, generate the design.md landing checklist; all tasks initially remain
// unchecked (-[ ]). Step3: Execute design.md item by item; only actually completed
// items may be marked -[x]; checking off in advance is strictly prohibited. For
// subsequent user input, first read design.md; if there are unfinished steps, first
// ask the user whether to continue the old plan or start a new task. All plans are
// persisted to files and cannot be stored only in memory."
var planModePrompt string = `用户开启Plan‑Mode。所有规划文件保存于:%s。
执行顺序不可跳过：
Step1:生成plan.md，内容包含：目标、需求点、各点实现方案。
Step2:由plan.md生成design.md落地清单，全部任务初始-[ ]未勾选。
Step3:逐条执行design.md，仅实际完成才可标记-[x]；严禁提前勾选。
后续用户输入时先读取design.md；若存在未完成步骤，先询问用户：继续完成旧计划，还是开启新任务。
所有计划持久化写入文件，不能仅存于内存。
`

// BuildSysPrompt 组装 system prompt：静态基础模板 + 技能索引段。
// 技能索引段由 context 包渲染，零技能时返回空串、整段省略。
func BuildSysPrompt(workDir string, skills []laxctx.Skill, isPlanMode bool, sessID string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(personalityPrompt, env.WorkDir))

	if index := laxctx.RenderSkillIndex(skills); index != "" {
		sb.WriteString("\n")
		sb.WriteString(index)
	}

	sessDir := filepath.Join(workDir, ".laxcode", ".session", sessID)
	if isPlanMode {
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf(planModePrompt, sessDir))
	}

	return sb.String()
}

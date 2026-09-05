package reactservice

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

// subAgentArgs 是 run_sub_agent 工具的入参：task 为独立子任务描述（必填），
// workDir 可选（缺省继承父 Agent 工作目录），abstract 为一句话摘要（仅用于
// BeforeExecInfo 的事件展示）。字段与老 engine.SubAgentArgs 一致。
type subAgentArgs struct {
	Task     string `json:"task"`
	WorkDir  string `json:"work_dir"`
	Abstract string `json:"abstract"`
}

// SubAgent 把「启动一个隔离子 Agent 跑子任务」包装成 tools.BaseTool 的适配器。
// 它编排一个子 ReActService：
//   - 全新子会话（id=sub:<ts>-<parentID>，复用父 Repo），历史独立，绝不写回父对话；
//   - 受限工具集（仅 bash + read_file，且不含 sub-agent 自身 → 天然防递归）；
//   - planMode=false，继承父的 LLMClient 与 tracer（子 span 树挂在同一 trace 下）；
//   - 事件静默（子 Agent 中间过程不外发）。
//
// 语义对齐老 internal/engine/subagent.go；置于 application 层（可依赖 domain），
// 且与 ReActService 同包，以复用其未导出的 tracer 字段。
type SubAgent struct {
	parent  *ReActService
	workDir string
}

// NewSubAgent 以父 ReActService 与工作目录构造子 Agent 工具。父的 LLMClient /
// tracer / Session.Repo 经 parent 复用；workDir 用于构建子 Agent 的受限工具集，
// 子任务可通过 work_dir 入参覆盖。调用方须在 parent 装配完成后注册本工具。
func NewSubAgent(parent *ReActService, workDir string) *SubAgent {
	return &SubAgent{parent: parent, workDir: workDir}
}

func (s *SubAgent) Name() string { return tools.ToolRunSubAgent }

func (s *SubAgent) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        s.Name(),
		Description: "启动一个独立子Agent去完成一项子任务。适合复杂、耗时、可以拆分出去的独立工作。不要用来执行简短命令。子Agent会自动生成报告，完成后返回结果。不要传入父对话全部历史。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "清晰、完整、独立的子任务描述。不要引用父对话模糊代词。任务必须不需要依赖父Agent上下文即可执行。",
				},
				"work_dir": map[string]any{
					"type":        "string",
					"description": "子Agent工作目录，不传默认继承父Agent工作目录",
				},
				"abstract": map[string]any{
					"type":        "string",
					"description": "一句话的子任务描述的摘要，50字以内",
				},
			},
			"required": []string{"task", "abstract"},
		},
	}
}

// Execute 解析子任务、装配一个隔离子 ReActService 跑完后返回其结论文本。
// 子 Agent 内部失败不返回 error（避免中断父的 ReAct 循环），而是把失败原因
// （若 Run 交回了部分产出则一并）作为工具结果字符串返回，父 Agent 可据此判断
// 补救方向——对齐老 subagent.go 的 (result, nil) 语义。仅入参解析 / 缺 task
// 这类调用方错误才返回真正的 error。
func (s *SubAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a subAgentArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing sub-agent args: %w", err)
	}
	if a.Task == "" {
		return "", fmt.Errorf("sub-agent task required")
	}
	workDir := s.workDir
	if a.WorkDir != "" {
		workDir = a.WorkDir
	}

	// 子会话：全新 id、复用父 Repo；不调用 Init（子会话从不续聊，无需加载历史）。
	// planMode=false，注入人格系统提示词（含 workDir 沙箱约束）。
	childID := "sub:" + time.Now().Format("20060102-150405.000") + "-" + s.parent.Session.ID
	childSess := session.NewSession(childID, s.parent.Session.Repo)
	if err := childSess.ReplaceSysPrompt(ctx, prompt.GetSysPrompt(workDir, childID, false)); err != nil {
		return fmt.Sprintf("sub agent failed to set sys prompt: %v", err), nil
	}

	// 受限工具集：仅 bash + read_file，不含 sub-agent 自身 → 防递归。子 Agent
	// 一次运行即完整生命周期，defer Close 回收 bash 后台进程与临时文件。
	childReg := tools.NewDefaultRegistry(s.parent.tracer)
	childReg.Register(tools.NewBashTool(workDir))
	childReg.Register(tools.NewReadFileTool(workDir))
	defer childReg.Close()

	// 事件静默：子 Agent 中间过程不外发（consumer 直接丢弃）。
	childSvc := NewReActService(childSess, s.parent.LLMClient, childReg, func(*ReactEvent) {}, s.parent.tracer)

	if err := childSess.AppendUserPrompt(ctx, a.Task); err != nil {
		return fmt.Sprintf("sub agent failed to append task: %v", err), nil
	}
	msg, err := childSvc.Run(ctx)
	if err != nil {
		// 失败交还父 Agent（不中断父循环）；当前 Run 出错时 msg 为 nil，
		// 若将来 Run 能交回部分产出，则一并附上供父判断补救方向。
		if msg != nil && msg.Content != "" {
			return fmt.Sprintf("sub agent failed: %v\npartial result: %s", err, msg.Content), nil
		}
		return fmt.Sprintf("sub agent failed: %v", err), nil
	}
	if msg == nil {
		return "", nil
	}
	return msg.Content, nil
}

// BeforeExecInfo 供父 Agent 的工具调用事件展示：带 abstract 摘要，缺省占位文案。
func (s *SubAgent) BeforeExecInfo(args json.RawMessage) string {
	var a subAgentArgs
	_ = json.Unmarshal(args, &a)
	if a.Abstract == "" {
		return "sub agent run to explore..."
	}
	return "sub agent run to explore: " + a.Abstract
}

// AfterExecInfo 子 Agent 结果已在 Execute 返回值中交还父 Agent，无需额外展示。
func (s *SubAgent) AfterExecInfo(json.RawMessage) string { return "" }

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

// SubAgentDeps 是子 Agent 派生受限工具集所需的领域端口集合，由组合根
// （cmd/agentasm）注入基础设施实现。集中在一个结构体里，使 application 层
// 只依赖 domain 端口、不反向依赖 infrastructure，且后续新增端口不破构造签名。
type SubAgentDeps struct {
	// WorkFS 是沙箱文件读写端口，供子 Agent 的 read_file 使用；无状态，可与父共享。
	WorkFS tools.WorkFS
	// NewShell 为每个子 Agent 新建一个独立的命令执行端口。**必须每次新建**：
	// 实现方持有自己派生的后台进程与输出临时文件登记表，子 Agent 结束时
	// childReg.Close() 会回收它；若与父共享同一实例，子 Agent 收尾会连带
	// 杀掉父 Agent 尚在运行的后台进程（如开发服务器）。
	NewShell func() tools.ShellRunner
	// SkillSrc 是技能定义文件的发现端口，供子 Agent 的系统提示词渲染技能索引；
	// 无状态且按调用传 workDir，可与父共享（子 Agent 可能跑在不同的 work_dir 下）。
	SkillSrc prompt.SkillSource
}

// SubAgent 把「启动一个隔离子 Agent 跑子任务」包装成 tools.BaseTool 的适配器。
// 它编排一个子 ReActService：
//   - 全新子会话（id=sub:<ts>-<parentID>，复用父 SessRepo），历史独立，绝不写回父对话；
//   - 受限工具集（仅 bash + read_file，且不含 sub-agent 自身 → 天然防递归）；
//   - planMode=false，继承父的 LLMClient 与 tracer（子 span 树挂在同一 trace 下）；
//   - 事件静默（子 Agent 中间过程不外发）。
//
// 语义对齐老 internal/engine/subagent.go；置于 application 层（可依赖 domain），
// 且与 ReActService 同包，以复用其未导出的 tracer 字段。
type SubAgent struct {
	parent  *ReActService
	workDir string
	deps    SubAgentDeps
}

// NewSubAgent 以父 ReActService、工作目录与端口集合构造子 Agent 工具。父的
// LLMClient / tracer / SessRepo 经 parent 复用；workDir 用于构建子 Agent 的
// 受限工具集，子任务可通过 work_dir 入参覆盖。调用方须在 parent 装配完成后
// 注册本工具。
func NewSubAgent(parent *ReActService, workDir string, deps SubAgentDeps) *SubAgent {
	return &SubAgent{parent: parent, workDir: workDir, deps: deps}
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
// 子 Agent 跑任务期间的失败（如 LLM 报错）不返回 error（避免中断父的 ReAct
// 循环），而是把失败原因（若交回了部分产出则一并）作为工具结果字符串返回，
// 父 Agent 可据此判断补救方向——对齐老 subagent.go 的 (result, nil) 语义。
// 入参解析 / 缺 task / 子会话初始化落盘失败这类环境级故障则返回真正的
// error：注册表会把它包成 IsError 的工具结果并记到 tool-exec span 上，
// 同样不会中断父循环，但失败原因不会被当作“子任务结论”掩盖。
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

	// 子会话：全新 id、复用父 SessRepo，使用与主 Agent 相同的快照恢复入口。
	// 注入人格系统提示词（含 workDir 沙箱约束）与子工作目录下的技能索引；
	// plan 传 nil（子 Agent 不支持 Plan Mode），warn 传 nil（技能警告已在主 Agent
	// 启动时针对主工作目录输出过，此处重复输出只会淹没子任务结果）。
	childID := "sub:" + time.Now().Format("20060102-150405.000") + "-" + s.parent.Session.ID
	childSess := session.NewSession(childID)
	childSkills := prompt.LoadSkills(s.deps.SkillSrc, workDir, nil)
	childSysPrompt := prompt.GetSysPrompt(workDir, childSkills, nil)

	// 受限工具集：bash + read_file；构造服务时另注册会话级 read_artifact。
	// 不含 sub-agent 自身，避免递归。子 Agent
	// 一次运行即完整生命周期，defer Close 回收 bash 后台进程与临时文件；
	// 命令执行端口按子 Agent 新建，以免回收波及父 Agent 的后台进程。
	childReg := tools.NewDefaultRegistry(s.parent.tracer)
	childReg.Register(tools.NewBashTool(workDir, s.deps.NewShell()))
	childReg.Register(tools.NewReadFileTool(workDir, s.deps.WorkFS))
	defer childReg.Close()

	// 事件静默：子 Agent 中间过程不外发（consumer 直接丢弃）。
	childSvc := NewReActService(childSess, s.parent.SessRepo, s.parent.LLMClient, childReg, func(*ReactEvent) {}, s.parent.tracer, s.parent.Artifacts)
	if err := childSvc.InitSession(ctx); err != nil {
		return "", fmt.Errorf("init session: %w", err)
	}
	if err := childSvc.InitSysPrompt(ctx, childSysPrompt); err != nil {
		return "", fmt.Errorf("init sys prompt: %w", err)
	}

	msg, err := childSvc.Chat(ctx, a.Task)
	if err != nil {
		// 失败交还父 Agent（不中断父循环）；当前 Chat 出错时 msg 为 nil，
		// 若将来 Chat 能交回部分产出，则一并附上供父判断补救方向。
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

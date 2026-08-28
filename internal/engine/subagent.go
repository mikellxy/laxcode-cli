package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
	"github.com/mikellxy/laxcode/internal/tracing"
)

type SubAgent struct {
	Parent *AgentEngine
}

func NewSubAgent(parent *AgentEngine) *SubAgent {
	return &SubAgent{
		Parent: parent,
	}
}

func (s *SubAgent) Name() string {
	return tools.ToolRunSubAgent
}

func (s *SubAgent) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
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

type SubAgentArgs struct {
	Task     string `json:"task"`
	WorkDir  string `json:"work_dir"`
	Abstract string `json:"abstract"`
}

func (s *SubAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var argsObj SubAgentArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}
	if argsObj.Task == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("task required"))
	}
	workDir := s.Parent.WorkDir
	if argsObj.WorkDir != "" {
		workDir = argsObj.WorkDir
	}

	id := "sub:" + time.Now().Format("20060102-150405.000") + "-" + s.Parent.Session.ID()
	// 子 Agent 继承父引擎的 Tracer：子 run 的 span 树经 ctx 嵌套在
	// 本次 sub_agent 的 tool-exec span 下，保持同一 trace
	reg := tools.NewDefaultRegistry(s.Parent.Printer, s.Parent.Tracer)
	reg.Register(tools.NewBashTool(workDir))
	reg.Register(tools.NewReadFileTool(workDir))
	sess := GetSession(workDir, id, false)
	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: argsObj.Task,
	})
	agentEngine := NewAgentEngine(reg,
		NewMonitoredProvider(provider.NewOpenApiProvider(provider.Info{}), sess),
		workDir,
		false,
		sess,
		s.Parent.Tracer,
	)
	agentEngine.Role = tracing.AgentRoleSub
	// 子 Agent 配色统一紫色，目的地继承父实例（one-shot 下随之静默/进 stderr）
	agentEngine.Printer = s.Parent.Printer.WithColors(printer.ColorPurple, printer.ColorPurple)

	result, err := agentEngine.Run(ctx)
	if err != nil {
		// 子 Agent 失败也要把已产出的部分结果交还父 Agent，供其判断补救方向
		return fmt.Sprintf("sub agent failed: %v", err), nil
	}
	return result, nil
}

func (s *SubAgent) BeforeExecInfo(message json.RawMessage) string {
	var argsObj SubAgentArgs
	_ = json.Unmarshal(message, &argsObj)
	if argsObj.Abstract == "" {
		return "sub agent run to explore..."
	}
	return "sub agent run to explore: " + argsObj.Abstract
}

func (s *SubAgent) AfterExecInfo(message json.RawMessage) string {
	return ""
}

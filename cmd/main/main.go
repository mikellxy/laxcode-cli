package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/config"
	"github.com/mikellxy/laxcode/internal/engine"
	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/tools"
)

func main() {
	// session id：命令行指定则续聊该会话；缺省以毫秒精度时间串新建，
	// 毫秒位避免同秒双开把两个会话写进同一目录。
	// 这些配置项只支持命令行，不走环境变量和 settings.json
	sessionID := config.String(config.Item{Flag: "SESSION", Usage: "session id to resume; empty starts a new session"})
	planMode := config.Bool(config.Item{Flag: "PLAN", Usage: "enable plan mode"})
	// one-shot 模式参数，同样只支持命令行：开关、提示词（文本或文件，
	// 文件优先）、工作目录（one-shot 必填）、verbose（中间过程进 stderr）
	oneShot := config.Bool(config.Item{Flag: "ONESHOT", Usage: "one-shot mode: run a single task and print structured JSON to stdout"})
	task := config.String(config.Item{Flag: "TASK", Usage: "one-shot task prompt text"})
	taskFile := config.String(config.Item{Flag: "TASK-FILE", Usage: "one-shot task prompt file path; takes precedence over -task"})
	workdir := config.String(config.Item{Flag: "WORKDIR", Usage: "working directory; required in one-shot mode, defaults to cwd otherwise"})
	verbose := config.Bool(config.Item{Flag: "VERBOSE", Usage: "one-shot mode: print intermediate progress to stderr"})
	if err := config.Parse(); err != nil {
		panic(err)
	}

	if oneShot.Get() {
		os.Exit(runOneShot(task.Get(), taskFile.Get(), workdir.Get(), sessionID.Get(), planMode.Get(), verbose.Get()))
	}

	workDir := workdir.Get()
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	agentEngine, id, err := assembleEngine(workDir, sessionID.Get(), planMode.Get())
	if err != nil {
		panic(err)
	}

	printer.Printf("starting LaxCode... work_dir: %s, session: %s, plan_mode: %v, debug: %v\n", workDir, id, planMode.Get(), config.Debug.Get())

	if err := engine.TerminalLoop(context.Background(), agentEngine); err != nil {
		panic(err)
	}
}

// runOneShot 是 one-shot 前端的 main 侧装配：设置输出闸门、校验参数、
// 装配引擎、执行 OneShotLoop，返回进程 exit code：
// 0 成功；1 运行失败（generate/too_many_turns）；2 用法错误。
// 错误一律走 stdout 的结构化 JSON + exit code，不 panic。
func runOneShot(taskText, taskFilePath, workDirFlag, sessionID string, planMode, verbose bool) int {
	// 输出闸门必须先于引擎装配：随后 NewAgentEngine/NewDefaultRegistry
	// 默认取 printer.Default()，散点警告同源，中间过程整体静默或进 stderr，
	// stdout 只剩 OneShotLoop 直写的契约 JSON。
	if verbose {
		printer.SetDefault(printer.NewWriterPrinter(os.Stderr, printer.ColorGray, printer.ColorGreen))
	} else {
		printer.SetDefault(printer.DiscardPrinter{})
	}

	usageFail := func(format string, args ...any) int {
		engine.WriteOneShotResult(os.Stdout, engine.NewUsageResult(fmt.Sprintf(format, args...)))
		return 2
	}

	if workDirFlag == "" {
		return usageFail("one-shot mode requires -workdir")
	}
	prompt, err := loadTaskPrompt(taskText, taskFilePath)
	if err != nil {
		return usageFail("read -task-file %s failed: %v", taskFilePath, err)
	}
	if prompt == "" {
		return usageFail("one-shot mode requires a non-empty prompt from -task or -task-file")
	}

	agentEngine, _, err := assembleEngine(workDirFlag, sessionID, planMode)
	if err != nil {
		return usageFail("init engine in %s failed: %v", workDirFlag, err)
	}

	if err := engine.OneShotLoop(context.Background(), agentEngine, prompt); err != nil {
		return 1
	}
	return 0
}

// loadTaskPrompt 解析 one-shot 任务提示词：-task-file 非空则读文件且
// 优先于 -task；两者取 TrimSpace 后判空。文件读取失败交由调用方判为
// 用法错误（路径错误属调用方问题，前置阶段可判定）。
func loadTaskPrompt(taskText, taskFilePath string) (string, error) {
	if taskFilePath != "" {
		data, err := os.ReadFile(taskFilePath)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(taskText), nil
}

// assembleEngine 完成两个前端 loop（TerminalLoop/OneShotLoop）共用的装配：
// session 目录创建、session id 决定、工具注册（含 sub agent）、会话加载
// （GetSession 重放历史，天然支持 -session 续聊）与监控 provider。
func assembleEngine(workDir, sessionID string, planMode bool) (*engine.AgentEngine, string, error) {
	if err := os.MkdirAll(filepath.Join(workDir, ".laxcode", ".session"), 0755); err != nil {
		return nil, "", err
	}

	id := sessionID
	if id == "" {
		id = time.Now().Format("20060102-150405.000")
	}

	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewBashTool(workDir))
	reg.Register(tools.NewWriteFileTool(workDir))
	reg.Register(tools.NewReadFileTool(workDir))
	reg.Register(tools.NewEditFileTool(workDir))
	sess := engine.GetSession(workDir, id, planMode)
	agentEngine := engine.NewAgentEngine(reg,
		engine.NewMonitoredProvider(provider.NewOpenApiProvider(provider.Info{}), sess),
		workDir,
		planMode,
		sess,
	)
	reg.Register(engine.NewSubAgent(agentEngine))
	return agentEngine, id, nil
}

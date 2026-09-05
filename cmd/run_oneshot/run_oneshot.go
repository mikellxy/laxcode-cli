// Package run_oneshot 是 one-shot 模式的前端入口：跑单个任务、把结构化结果以
// 单行 JSON 直写 stdout，供脚本 / CI 消费。它与交互模式（cmd/run_cli）平级，
// 共用 application/reactservice 的 ReAct 循环与 domain/infrastructure 组件，
// 仅依赖 DDD 三层（application / infrastructure / domain），不引用老的非 DDD 代码。
//
// 本包对齐老 cmd/main/main_bak.go:runOneShot 的契约（结果 schema、exit code），
// 差异在于中间过程的分流方式：老实现靠全局 printer 闸门导向 stderr/discard，
// 本实现改用 ReActService 的事件回调分流，天然不污染 stdout。
package run_oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// 进程 exit code：供 main 直接 os.Exit 映射，与老 runOneShot 一致。
const (
	exitOK    = 0 // 成功
	exitRun   = 1 // 运行失败（模型调用 / 会话写入等）
	exitUsage = 2 // 用法错误（参数 / 配置缺失，发生在跑任务之前）
)

// one-shot 结构化返回中 error.type 的机器可读值。新 ReAct 循环无「工具轮次上限」
// 概念（跑到无工具调用为止），故不含老架构的 too_many_turns。
const (
	// ErrTypeUsage 参数 / 配置用法错误：发生在任务执行之前，session_id 为空、
	// token 统计为零值。
	ErrTypeUsage = "usage"
	// ErrTypeGenerate 运行失败：模型调用或会话写入出错。
	ErrTypeGenerate = "generate"
)

// OneShotError 是结构化返回 error 字段的载荷：type 机器可读、message 供调用方
// 诊断（用法错误须能据此修正调用参数）。
type OneShotError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// OneShotResult 是 one-shot 向 stdout 输出的扁平同构结构：成功与失败共用一套
// schema（error 为 null 即成功），失败也带 token 统计——计费已发生，调用方有权知道。
type OneShotResult struct {
	SessionID   string                       `json:"session_id"`
	Result      string                       `json:"result"`
	TokenUsed   sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken sharedkernel.TokenStatistics `json:"window_token"`
	Error       *OneShotError                `json:"error"`
}

// Run 执行 one-shot 模式并返回进程 exit code（0 成功 / 1 运行失败 / 2 用法错误）。
//
// 契约：stdout 只承载单行结果 JSON；中间过程（thinking / 生成 / 工具调用）整体
// 丢弃（见 newEventConsumer）。错误一律走 stdout 的结构化 JSON + exit code，不
// panic——用法错误在跑任务前即可判定，运行失败则带已发生的 token 统计。
//
// 装配（session / tracer / tools / provider / ReActService）经 cmd/agentasm 组合根
// 与交互模式 run_cli.Run 共用；本函数只保留 one-shot 专属的「参数校验」与「结果
// 契约输出」两段逻辑。
func Run() int {
	ctx := context.Background()
	cli := config.CliConf
	env := config.EnvAndFileConf

	// usageFail 统一用法错误出口：写 usage JSON 到 stdout 并返回 exit 2。
	usageFail := func(format string, args ...any) int {
		writeResult(os.Stdout, OneShotResult{
			Error: &OneShotError{Type: ErrTypeUsage, Message: fmt.Sprintf(format, args...)},
		})
		return exitUsage
	}

	// 前置校验：workdir 必填、openai 三项配置必填、任务提示词非空。全部归为
	// usage 错误（exit 2），发生在 session 建立之前，结果中 session_id 为空。
	if cli.WorkDir == "" {
		return usageFail("one-shot mode requires -workdir")
	}
	if env.OpenaiApiKey == "" || env.OpenaiBaseUrl == "" || env.OpenaiModel == "" {
		return usageFail("openai_api_key / openai_base_url / openai_model are required")
	}
	taskPrompt, err := loadTaskPrompt(cli.Task, cli.TaskFile)
	if err != nil {
		return usageFail("read -task-file %s failed: %v", cli.TaskFile, err)
	}
	if taskPrompt == "" {
		return usageFail("one-shot mode requires a non-empty prompt from -task or -task-file")
	}

	// 装配（会话 / tracer / 工具集含子 Agent / provider / ReActService）收口到
	// cmd/agentasm 组合根，与交互模式共用；one-shot 专属的只有前后的参数校验与结果
	// 契约输出。Consumer 用静默丢弃回调，保证 stdout 只承载结果 JSON。
	assembled, err := agentasm.Assemble(ctx, agentasm.Input{
		WorkDir:   cli.WorkDir,
		SessionID: cli.Session,
		PlanMode:  cli.Plan,
		Consumer:  newEventConsumer(),
	})
	if err != nil {
		return usageFail("assemble agent failed: %v", err)
	}
	defer assembled.Cleanup()
	sess := assembled.Session

	// 追加 task 为 user 消息后执行一次 ReAct 循环，直到模型给出无工具调用的最终回答。
	if err := sess.AppendUserPrompt(ctx, taskPrompt); err != nil {
		return usageFail("append task prompt failed: %v", err)
	}
	msg, runErr := assembled.Service.Run(ctx)

	// 结果契约：成功 / 失败共用同一 schema，均带 session_id 与 token 统计。
	res := OneShotResult{
		SessionID:   sess.ID,
		TokenUsed:   sess.TokenUsed,
		WindowToken: sess.WindowToken,
	}
	if runErr != nil {
		res.Error = &OneShotError{Type: ErrTypeGenerate, Message: runErr.Error()}
		writeResult(os.Stdout, res)
		return exitRun
	}
	if msg != nil {
		res.Result = msg.Content
	}
	writeResult(os.Stdout, res)
	return exitOK
}

// newEventConsumer 返回 one-shot 的 ReAct 事件回调：始终丢弃中间过程（thinking /
// 生成 / 工具调用），保证 stdout 只承载结果 JSON、契约纯净。作为 Consumer 注入
// cmd/agentasm 的装配。
func newEventConsumer() func(*reactservice.ReactEvent) {
	return func(*reactservice.ReactEvent) {}
}

// loadTaskPrompt 解析任务提示词：-task-file 非空则读文件且优先于 -task；两者取
// TrimSpace 后判空。文件读取失败交由调用方判为用法错误（路径错误属调用方问题，
// 前置阶段即可判定）。与老 main_bak.go:loadTaskPrompt 逻辑一致。
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

// writeResult 把结果序列化为单行 JSON（带换行）写入 w。字段全为可序列化类型，
// marshal 失败仅剩理论可能；此时经 stderr 告警而不中断，保证 stdout 契约不被污染。
func writeResult(w io.Writer, res OneShotResult) {
	data, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal one-shot result failed: %v\n", err)
		return
	}
	fmt.Fprintln(w, string(data))
}

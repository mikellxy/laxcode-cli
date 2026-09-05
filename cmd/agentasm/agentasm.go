package agentasm

// Package agentasm 是 cmd 层的组合根（composition root）：把交互模式
// （cmd/run_cli）与 one-shot 模式（cmd/run_oneshot）共用的 Agent 装配逻辑收口到
// Assemble，消除两处重复。装配产物是一个可直接 Run 的 ReActService 及其会话与
// 清理钩子；两端各自的输入解析、校验、事件呈现与主循环仍留在前端。
//
// 之所以独立成包而非放进 cmd/main：main 是 package main，不可被导入，且它已
// import 两个前端，反向依赖会成环。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	_ "github.com/mikellxy/laxcode/internal/infrastructure/tracing/custom"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
	"go.opentelemetry.io/otel/trace"
)

// Input 是装配 ReActService 所需、且因前端而异的输入。
type Input struct {
	// WorkDir 是 Agent 工作目录（沙箱根）：交互模式取 cwd，one-shot 取 -workdir。
	WorkDir string
	// SessionID 为空则由 session 层以毫秒精度时间串新建；非空则续聊该会话。
	SessionID string
	// PlanMode 为真时在系统提示词追加 Plan Mode 工作流段。
	PlanMode bool
	// Consumer 是 ReAct 事件回调：交互模式接 stdout 彩色打印，one-shot 接静默丢弃。
	Consumer func(*reactservice.ReactEvent)
}

// Assembled 是装配产物。
type Assembled struct {
	// Service 是可直接 Run 的主 Agent 服务（已注册 bash/write/read/edit + 子 Agent）。
	Service *reactservice.ReActService
	// Session 是 Service 持有的主会话，供前端读取 ID / token 统计、追加 user 消息。
	Session *session.Session
	// Cleanup 回收带生命周期的资源，调用方 defer 一次；以 sync.Once 保证幂等，
	// 使信号处理与正常退出路径可各自安全调用。顺序：先 Close 工具注册表（回收
	// bash 后台进程与临时文件），再 Shutdown tracer（flush 关闭阶段产生的 span）。
	Cleanup func()
}

// Assemble 装配一个可直接运行的 ReActService：会话（含系统提示词）、tracer、
// 工具集（含子 Agent）、LLM provider。OpenAI 凭据取自 config.EnvAndFileConf，
// 调用前须已由调用方校验（本函数不重复校验，缺失会在 Run 时才暴露）。
// 返回的 error 仅来自会话初始化 / 系统提示词写入。
func Assemble(ctx context.Context, in Input) (*Assembled, error) {
	// session：repo 落在 ${workDir}/.laxcode/.session；SessionID 为空则新建。
	repo := sessionrepo.NewFsSessionRepo(filepath.Join(in.WorkDir, ".laxcode", ".session"))
	sess := session.NewSession(in.SessionID, repo)
	if err := sess.Init(); err != nil {
		return nil, err
	}
	if err := sess.ReplaceSysPrompt(ctx, prompt.GetSysPrompt(in.WorkDir, sess.ID, in.PlanMode)); err != nil {
		return nil, err
	}

	// tracer：HandleDB 命中（custom 包 init 注册）优先，否则 filetrace 落盘到
	// ${workDir}/.laxcode/.session/${sessID}/log/tracing.log；无法创建回退 noop。
	// 先查 HandleDB 再决定是否创建 filetrace，避免命中注册项时仍打开日志文件造成句柄泄漏。
	var traceHandle *tracing.Handle
	for _, h := range tracing.HandleDB {
		traceHandle = h
		break
	}
	if traceHandle == nil {
		logPath := filepath.Join(in.WorkDir, ".laxcode", ".session", sess.ID, "log", "tracing.log")
		traceHandle = newTraceHandle(logPath)
	}
	tracer := traceHandle.Tracer

	// tools：默认工具集；子 Agent 须在 svc 建好后注册进同一 registry（见下）。
	toolReg := tools.NewDefaultRegistry(tracer)
	toolReg.Register(tools.NewBashTool(in.WorkDir))
	toolReg.Register(tools.NewWriteFileTool(in.WorkDir))
	toolReg.Register(tools.NewReadFileTool(in.WorkDir))
	toolReg.Register(tools.NewEditFileTool(in.WorkDir))

	// provider + service
	c := config.EnvAndFileConf
	llmClient := llmprovider.NewOpenApiProvider(c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel)
	svc := reactservice.NewReActService(sess, llmClient, toolReg, in.Consumer, tracer)
	// 子 Agent 复用 svc 的 LLMClient/tracer/Repo 派生隔离子服务，注册进同一
	// toolReg（svc 持其引用，late register 对 svc 可见）。
	toolReg.Register(reactservice.NewSubAgent(svc, in.WorkDir))

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = toolReg.Close()
			_ = traceHandle.Shutdown(ctx)
		})
	}

	return &Assembled{Service: svc, Session: sess, Cleanup: cleanup}, nil
}

// newTraceHandle 按 logPath 构造默认 filetrace Provider；日志文件无法创建（如目录
// 无写权限）时回退官方 noop 并在 stderr 提示，不中断装配。
func newTraceHandle(logPath string) *tracing.Handle {
	var tp trace.TracerProvider
	if f, err := filetrace.New(logPath); err == nil {
		tp = f
	} else {
		fmt.Fprintf(os.Stderr, "filetrace: %v; tracing disabled\n", err)
	}
	return tracing.New(tp)
}

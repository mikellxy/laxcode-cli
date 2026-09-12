package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mikellxy/laxcode/cmd/run_cli"
	"github.com/mikellxy/laxcode/cmd/run_oneshot"
	"github.com/mikellxy/laxcode/cmd/run_sse"
	applicationrouter "github.com/mikellxy/laxcode/internal/application/llm_router"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infrastructurerouter "github.com/mikellxy/laxcode/internal/infrastructure/llmrouter"
)

const llmRouterShutdownTimeout = 5 * time.Second

func main() {
	logFile, err := configureSlog(appLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", appLogPath, err)
		os.Exit(1)
	}
	defer logFile.Close()

	if err := config.ParseEnvAndFile(); err != nil {
		panic(err)
	}
	if err := config.ParseCli(); err != nil {
		panic(err)
	}

	// 模型路由器独立使用 llmrouter.log；任何启动模式都先在 goroutine 中启动
	// 本地 HTTP server，再把实际端点写入运行时配置供 agentasm 注入 provider。
	routerLogger, routerLogFile, err := newFileLogger(llmRouterLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", llmRouterLogPath, err)
		_ = logFile.Close()
		os.Exit(1)
	}
	routerServer := applicationrouter.NewHTTPServer(infrastructurerouter.NewOpenAIStreamClient(
		config.EnvAndFileConf.OpenaiApiKey,
		config.EnvAndFileConf.OpenaiBaseUrl,
		config.EnvAndFileConf.OpenaiModel,
	), routerLogger)
	runningRouter, err := routerServer.Start(config.EnvAndFileConf.LlmRouterAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = routerLogFile.Close()
		_ = logFile.Close()
		os.Exit(1)
	}
	config.EnvAndFileConf.LlmRouterURL = runningRouter.Endpoint()
	shutdownRouter := func() {
		ctx, cancel := context.WithTimeout(context.Background(), llmRouterShutdownTimeout)
		defer cancel()
		if err := runningRouter.Shutdown(ctx); err != nil {
			routerLogger.Error("llmrouter_shutdown_failed", "error", err)
		}
		_ = routerLogFile.Close()
	}
	defer shutdownRouter()

	// 三态分发，优先级 oneshot > sse > cli：oneshot 保留原有 os.Exit 契约，
	// sse 起阻塞式 HTTP 服务，二者皆未开启时进入默认 TUI 交互模式。
	switch {
	case config.CliConf.Oneshot:
		// one-shot：跑单个任务、结果 JSON 直写 stdout，Run 返回进程 exit code
		// （0 成功 / 1 运行失败 / 2 用法错误）。经 os.Exit 映射；Run 内部的
		// defer（工具回收 / trace flush）在返回前已执行，不受 os.Exit 跳过影响。
		exitCode := run_oneshot.Run()
		shutdownRouter()
		_ = logFile.Close() // os.Exit 不执行 defer，显式关闭。
		os.Exit(exitCode)
	case config.CliConf.SSE:
		// sse server：阻塞式监听，接受 POST /chat 并把 ReAct 事件以 SSE 流式回传；
		// SIGINT/SIGTERM 触发优雅关闭后 Run 返回。
		run_sse.Run()
	default:
		run_cli.Run()
	}
}

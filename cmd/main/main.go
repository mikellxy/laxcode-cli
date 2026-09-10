package main

import (
	"os"

	"github.com/mikellxy/laxcode/cmd/run_cli"
	"github.com/mikellxy/laxcode/cmd/run_oneshot"
	"github.com/mikellxy/laxcode/cmd/run_sse"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func main() {
	if err := config.ParseEnvAndFile(); err != nil {
		panic(err)
	}
	if err := config.ParseCli(); err != nil {
		panic(err)
	}

	// 三态分发，优先级 oneshot > sse > cli：oneshot 保留原有 os.Exit 契约，
	// sse 起阻塞式 HTTP 服务，二者皆未开启时进入默认 TUI 交互模式。
	switch {
	case config.CliConf.Oneshot:
		// one-shot：跑单个任务、结果 JSON 直写 stdout，Run 返回进程 exit code
		// （0 成功 / 1 运行失败 / 2 用法错误）。经 os.Exit 映射；Run 内部的
		// defer（工具回收 / trace flush）在返回前已执行，不受 os.Exit 跳过影响。
		os.Exit(run_oneshot.Run())
	case config.CliConf.SSE:
		// sse server：阻塞式监听，接受 POST /chat 并把 ReAct 事件以 SSE 流式回传；
		// SIGINT/SIGTERM 触发优雅关闭后 Run 返回。
		run_sse.Run()
	default:
		run_cli.Run()
	}
}

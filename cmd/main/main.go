package main

import (
	"os"

	"github.com/mikellxy/laxcode/cmd/run_cli"
	"github.com/mikellxy/laxcode/cmd/run_oneshot"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func main() {
	if err := config.ParseEnvAndFile(); err != nil {
		panic(err)
	}
	if err := config.ParseCli(); err != nil {
		panic(err)
	}

	if config.CliConf.Oneshot {
		// one-shot：跑单个任务、结果 JSON 直写 stdout，Run 返回进程 exit code
		// （0 成功 / 1 运行失败 / 2 用法错误）。经 os.Exit 映射；Run 内部的
		// defer（工具回收 / trace flush）在返回前已执行，不受 os.Exit 跳过影响。
		os.Exit(run_oneshot.Run())
	} else {
		run_cli.Run()
	}
}

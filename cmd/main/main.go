package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mikellxy/laxcode/internal/engine"
	"github.com/mikellxy/laxcode/internal/env"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/tools"
)

func main() {
	workDir, _ := os.Getwd()

	// make session dir
	if err := os.MkdirAll(".laxcode/.session", 0755); err != nil {
		panic(err)
	}

	// session id：命令行指定则续聊该会话；缺省以毫秒精度时间串新建，
	// 毫秒位避免同秒双开把两个会话写进同一目录
	sessionID := flag.String("session", "", "session id to resume; empty starts a new session")
	planMode := flag.Bool("plan", false, "enable plan mode")
	debug := flag.Bool("debug", false, "enable debug mode")
	flag.Parse()

	id := *sessionID
	if id == "" {
		id = time.Now().Format("20060102-150405.000")
	}
	if err := env.LoadConfig(*debug); err != nil {
		panic(err)
	}

	engine.InitSessionDB()

	reg := tools.NewDefaultRegistry()
	reg.Register(tools.NewBashTool(workDir))
	reg.Register(tools.NewWriteFileTool(workDir))
	reg.Register(tools.NewReadFileTool(workDir))
	reg.Register(tools.NewEditFileTool(workDir))
	agentEngine := engine.NewAgentEngine(reg,
		provider.NewOpenApiProvider(provider.Info{Name: "deepseek"}),
		workDir,
		*planMode,
		id,
	)
	reg.Register(engine.NewSubAgent(agentEngine))

	fmt.Printf("starting LaxCode... work_dir: %s, session: %s, plan_mode: %v, debug: %v\n", workDir, id, planMode, env.Debug)

	err := engine.TerminalLoop(context.Background(), agentEngine)
	if err != nil {
		panic(err)
	}
}

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mikellxy/laxcode/internal/config"
	"github.com/mikellxy/laxcode/internal/engine"
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
	// 毫秒位避免同秒双开把两个会话写进同一目录。
	// 这三个配置项只支持命令行，不走环境变量和 settings.json
	sessionID := config.String(config.Item{Flag: "SESSION", Usage: "session id to resume; empty starts a new session"})
	planMode := config.Bool(config.Item{Flag: "PLAN", Usage: "enable plan mode"})
	if err := config.Parse(); err != nil {
		panic(err)
	}

	id := sessionID.Get()
	if id == "" {
		id = time.Now().Format("20060102-150405.000")
	}

	engine.InitSessionDB()

	reg := tools.NewDefaultRegistry()
	reg.Register(tools.NewBashTool(workDir))
	reg.Register(tools.NewWriteFileTool(workDir))
	reg.Register(tools.NewReadFileTool(workDir))
	reg.Register(tools.NewEditFileTool(workDir))
	agentEngine := engine.NewAgentEngine(reg,
		provider.NewOpenApiProvider(provider.Info{}),
		workDir,
		planMode.Get(),
		id,
	)
	reg.Register(engine.NewSubAgent(agentEngine))

	fmt.Printf("starting LaxCode... work_dir: %s, session: %s, plan_mode: %v, debug: %v\n", workDir, id, planMode.Get(), config.Debug.Get())

	err := engine.TerminalLoop(context.Background(), agentEngine)
	if err != nil {
		panic(err)
	}
}

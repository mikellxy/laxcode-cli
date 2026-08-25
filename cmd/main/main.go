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
	env.WorkDir, _ = os.Getwd()

	// session id：命令行指定则续聊该会话；缺省以毫秒精度时间串新建，
	// 毫秒位避免同秒双开把两个会话写进同一目录
	sessionID := flag.String("session", "", "session id to resume; empty starts a new session")
	flag.Parse()

	id := *sessionID
	if id == "" {
		id = time.Now().Format("20060102-150405.000")
	}

	engine.InitSessionDB(env.WorkDir, id)

	agentAgent := engine.NewAgentEngine(tools.NewDefaultRegistry(),
		provider.NewOpenApiProvider(provider.Info{Name: "deepseek"}),
		env.WorkDir,
	)

	fmt.Printf("starting LaxCode... work_dir: %s, session: %s\n", env.WorkDir, id)

	err := agentAgent.TerminalLoop(context.Background(), id)
	if err != nil {
		panic(err)
	}
}

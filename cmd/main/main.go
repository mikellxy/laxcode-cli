package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mikellxy/laxcode/internal/engine"
	"github.com/mikellxy/laxcode/internal/env"
	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/tools"
)

func main() {
	env.WorkDir, _ = os.Getwd()

	agentAgent := engine.NewAgentEngine(tools.NewDefaultRegistry(),
		provider.NewOpenApiProvider(provider.Info{Name: "deepseek"}),
		env.WorkDir,
	)

	fmt.Printf("starting LaxCode... work_dir: %s\n", env.WorkDir)

	err := agentAgent.Loop(context.Background())
	if err != nil {
		panic(err)
	}
}

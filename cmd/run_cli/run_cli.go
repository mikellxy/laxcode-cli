package run_cli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/infrastructure/cliprinter"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

const (
	ColorReset  = "\033[0m"
	ColorGray   = "\033[90m"
	ColorGreen  = "\033[32m"
	ColorPurple = "\033[35m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorBlue   = "\033[34m"
)

func checkConfig() error {
	if config.EnvAndFileConf.OpenaiApiKey == "" {
		return errors.New("openai_api_key is required")
	}
	if config.EnvAndFileConf.OpenaiBaseUrl == "" {
		return errors.New("openai_base_url is required")
	}
	if config.EnvAndFileConf.OpenaiModel == "" {
		return errors.New("openai_model is required")
	}
	return nil
}

func Run() {
	printer := cliprinter.NewDefaultPrinter()

	if err := checkConfig(); err != nil {
		printer.Fatal(err)
	}

	ctx := context.Background()

	workDir, err := os.Getwd()
	if err != nil {
		printer.Fatal(err)
	}

	// rcf：交互模式的事件呈现——把 ReAct 中间过程彩色打印到 stdout。作为 Consumer
	// 注入装配，与 one-shot 的静默丢弃回调形成对照。
	rcf := func(e *reactservice.ReactEvent) {
		switch e.Type {
		case reactservice.ReActEventTypeReasoning:
			printer.Printf("%s[LaxCode] thinking: %s%s\n", ColorGray, e.Content, ColorReset)
		case reactservice.ReActEventTypeMsg:
			printer.Printf("%s[LaxCode] LLM generates: %s%s\n", ColorGreen, e.Content, ColorReset)
		case reactservice.ReActEventTypeToolCall:
			printer.Printf("%s[LaxCode] tool execute... %s%s\n", ColorYellow, e.Content, ColorReset)
		}
	}

	// 装配（会话 / tracer / 工具集含子 Agent / provider / ReActService）收口到
	// cmd/agentasm 组合根，与 one-shot 共用；本函数只保留交互模式专属的信号处理与
	// REPL 循环。Cleanup 幂等：正常退出（defer）与信号退出（goroutine）各调一次安全。
	assembled, err := agentasm.Assemble(ctx, agentasm.Input{
		WorkDir:   workDir,
		SessionID: config.CliConf.Session,
		PlanMode:  config.CliConf.Plan,
		Consumer:  rcf,
	})
	if err != nil {
		printer.Fatal(err)
	}
	defer assembled.Cleanup()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		assembled.Cleanup()
		os.Exit(130)
	}()

	scanner := bufio.NewScanner(os.Stdin)
	printer.Printf("session_id: %s\n", assembled.Session.ID)
	printer.Printf(">>> Agent ready, input your question\n")
	for {
		printer.Printf("%s> %s", ColorBlue, ColorReset)
		if !scanner.Scan() {
			// EOF
			printer.Printf("\nreceive EOF, exit\n")
			return
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}

		if err := assembled.Session.AppendUserPrompt(ctx, userInput); err != nil {
			printer.Fatal(err)
		}
		_, err := assembled.Service.Run(ctx)
		if err != nil {
			printer.Fatal(err)
		}
	}
}

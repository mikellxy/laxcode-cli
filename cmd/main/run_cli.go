package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/cliprinter"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
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
	if config.Config.OpenaiApiKey == "" {
		return errors.New("openai_api_key is required")
	}
	if config.Config.OpenaiBaseUrl == "" {
		return errors.New("openai_base_url is required")
	}
	if config.Config.OpenaiModel == "" {
		return errors.New("openai_model is required")
	}
	return nil
}

func main() {
	printer := cliprinter.NewDefaultPrinter()

	if err := checkConfig(); err != nil {
		printer.Fatal(err)
	}

	ctx := context.Background()

	workDir, err := os.Getwd()
	if err != nil {
		printer.Fatal(err)
	}
	sessionDir := filepath.Join(workDir, ".laxcode", ".session")
	sessionRepo := sessionrepo.NewFsSessionRepo(sessionDir)
	sess := session.NewSession("", sessionRepo)
	if err := sess.Init(); err != nil {
		printer.Fatal(err)
	}
	sess.ReplaceSysPrompt(ctx, prompt.GetSysPrompt())

	c := config.Config
	llmProvider := llmprovider.NewOpenApiProvider(c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel)

	toolReg := tools.NewDefaultRegistry()
	toolReg.Register(tools.NewBashTool(workDir))
	toolReg.Register(tools.NewWriteFileTool(workDir))
	toolReg.Register(tools.NewReadFileTool(workDir))
	toolReg.Register(tools.NewEditFileTool(workDir))
	defer toolReg.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		toolReg.Close()
		os.Exit(130)
	}()

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

	reActSvc := reactservice.NewReActService(
		sess,
		llmProvider,
		toolReg,
		rcf)

	scanner := bufio.NewScanner(os.Stdin)
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

		if err := reActSvc.Session.AppendUserPrompt(ctx, userInput); err != nil {
			printer.Fatal(err)
		}
		_, err := reActSvc.Run(ctx)
		if err != nil {
			printer.Fatal(err)
		}
	}
}

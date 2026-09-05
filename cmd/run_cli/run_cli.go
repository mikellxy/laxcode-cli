package run_cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	_ "github.com/mikellxy/laxcode/internal/infrastructure/tracing/custom"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
	"go.opentelemetry.io/otel/trace"
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

// newCliTraceHandle 按 logPath 构造默认的 filetrace Provider；日志文件无法
// 创建时回退 noop 并在 stderr 提示。HandleDB 非空时由调用方覆盖为自定义 Handle。
// 与老 main.go 的 newTraceHandle 区分命名，避免同包符号冲突。
func newCliTraceHandle(logPath string) *tracing.Handle {
	var tp trace.TracerProvider
	if f, err := filetrace.New(logPath); err == nil {
		tp = f
	} else {
		fmt.Fprintf(os.Stderr, "filetrace: %v; tracing disabled\n", err)
	}
	return tracing.New(tp)
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
	sessionDir := filepath.Join(workDir, ".laxcode", ".session")
	sessionRepo := sessionrepo.NewFsSessionRepo(sessionDir)
	sess := session.NewSession(config.CliConf.Session, sessionRepo)
	if err := sess.Init(); err != nil {
		printer.Fatal(err)
	}
	if err := sess.ReplaceSysPrompt(ctx, prompt.GetSysPrompt(workDir)); err != nil {
		printer.Fatal(err)
	}

	c := config.EnvAndFileConf
	llmProvider := llmprovider.NewOpenApiProvider(c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel)

	// tracer 装配：HandleDB 非空时优先用 custom 包 init 注册的 Handle；否则
	// 默认用 filetrace 落盘到 ${workDir}/.laxcode/.session/${sessID}/log/tracing.log。
	// 先查 HandleDB 再决定是否创建 filetrace，避免命中注册项时仍打开日志文件
	// 造成句柄泄漏。defer Shutdown 先于 toolReg.Close 注册，故最后执行，确保
	// 工具关闭阶段的 span 也被 flush。
	var traceHandle *tracing.Handle
	for _, h := range tracing.HandleDB {
		traceHandle = h
		break
	}
	if traceHandle == nil {
		logPath := filepath.Join(workDir, ".laxcode", ".session", sess.ID, "log", "tracing.log")
		traceHandle = newCliTraceHandle(logPath)
	}
	defer func() { _ = traceHandle.Shutdown(ctx) }()
	tracer := traceHandle.Tracer

	toolReg := tools.NewDefaultRegistry(tracer)
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
		rcf,
		tracer)

	scanner := bufio.NewScanner(os.Stdin)
	printer.Printf("session_id: %s\n", sess.ID)
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

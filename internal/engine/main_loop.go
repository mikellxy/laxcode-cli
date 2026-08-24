package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/tools"
)

// errTooManyTurns 是 Run 触发单轮工具循环上限的 sentinel error，
// Loop 依赖 errors.Is 识别它并继续等待下一轮输入，而不是终止 REPL
var errTooManyTurns = errors.New("too many turns")

type AgentEngine struct {
	ToolRegistry tools.Registry
	Provider     provider.Provider
	WorkingDir   string
	contextHis   []schema.Message
}

func NewAgentEngine(toolRegistry tools.Registry, provider provider.Provider, workDir string) *AgentEngine {
	return &AgentEngine{
		ToolRegistry: toolRegistry,
		Provider:     provider,
		WorkingDir:   workDir,
	}
}

func (f *AgentEngine) Loop(ctx context.Context) error {
	// 初始化system prompt，只做一次！后续多轮用户输入不再重复加system
	f.contextHis = append(f.contextHis, schema.Message{
		Role:    schema.RoleSystem,
		Content: BuildSysPrompt(f.WorkingDir),
	})

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println(">>> Agent ready, input your question, input exit to quit")

	for {
		fmt.Print("\033[34m> \033[0m")
		if !scanner.Scan() {
			// EOF
			fmt.Println("\nreceive EOF, exit")
			return nil
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}
		if strings.ToLower(userInput) == "exit" {
			fmt.Println("quit agent")
			return nil
		}

		// user prompy
		f.contextHis = append(f.contextHis, schema.Message{
			Role:    schema.RoleUser,
			Content: userInput,
		})

		err := f.Run(ctx)
		if err != nil {
			// warn too many turns
			if errors.Is(err, errTooManyTurns) {
				fmt.Printf("[warn] %v\n", err)
			} else {
				return fmt.Errorf("run llm tool loop: %w", err)
			}
		}
	}
}

func (f *AgentEngine) Run(ctx context.Context) error {
	contextHis := f.contextHis
	// contextHis 只复制了 slice 头，与 f.contextHis 共享底层数组；
	// 本轮累积的 assistant 回复与工具结果必须写回 f.contextHis，
	// 否则下一轮模型看不到历史回复，会把旧问题重新回答一遍
	defer func() { f.contextHis = contextHis }()

	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 50 {
			return errTooManyTurns
		}
		msgs, err := f.Provider.Generate(ctx, contextHis, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return fmt.Errorf("generating message: %w", err)
		}

		var toolCallCnt int
		for _, msg := range msgs {
			if len(msg.Content) > 0 {
				fmt.Printf("\033[32m[LaxCode] LLM generates: %s\033[0m\n", msg.Content)
			}

			toolCallCnt += len(msg.ToolCalls)

			contextHis = append(contextHis, msg)

			for _, toolCall := range msg.ToolCalls {
				toolResult := f.ToolRegistry.Execute(ctx, &toolCall)
				contextHis = append(contextHis, schema.Message{
					Role:       schema.RoleUser,
					Content:    toolResult.Output,
					ToolCallID: toolResult.ToolCallID,
				})
			}
		}
		if toolCallCnt == 0 {
			break
		}
	}

	return nil
}

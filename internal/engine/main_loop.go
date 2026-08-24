package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
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
}

func NewAgentEngine(toolRegistry tools.Registry, provider provider.Provider, workDir string) *AgentEngine {
	return &AgentEngine{
		ToolRegistry: toolRegistry,
		Provider:     provider,
		WorkingDir:   workDir,
	}
}

func (f *AgentEngine) Loop(ctx context.Context, sessionID string) error {
	sess := getSession(sessionID)
	if sess == nil {
		return fmt.Errorf("session %q not found in session db (InitSessionDB not called?)", sessionID)
	}

	// 初始化system prompt，只做一次！后续多轮用户输入不再重复加system；
	// 技能索引随启动时加载一次并注入，会话内不刷新。system prompt 不落盘，
	// 每次启动重建，续聊时模板/技能变更即时生效
	sysPrompt := BuildSysPrompt(f.WorkingDir, laxctx.LoadSkills(f.WorkingDir))

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
		sess.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: userInput,
		})

		err := f.Run(ctx, sess, sysPrompt)
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

func (f *AgentEngine) Run(ctx context.Context, sess *Session, sysPrompt string) error {
	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 50 {
			return errTooManyTurns
		}
		// 历史唯一真相源是 session：Generate 前每轮重拼视图（system + 已有
		// 历史 + 本轮新消息），产生的消息一律经 Append 写回，无 slice 别名回写
		msgs, err := f.Provider.Generate(ctx, sess.View(sysPrompt), f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return fmt.Errorf("generating message: %w", err)
		}

		var toolCallCnt int
		for _, msg := range msgs {
			if len(msg.Content) > 0 {
				fmt.Printf("\033[32m[LaxCode] LLM generates: %s\033[0m\n", msg.Content)
			}

			toolCallCnt += len(msg.ToolCalls)

			sess.Append(msg)

			for _, toolCall := range msg.ToolCalls {
				toolResult := f.ToolRegistry.Execute(ctx, &toolCall)
				sess.Append(schema.Message{
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

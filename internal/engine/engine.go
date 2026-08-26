package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/env"
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

func (f *AgentEngine) TerminalLoop(ctx context.Context, sessionID string) error {
	sess := getSession(sessionID)
	if sess == nil {
		return fmt.Errorf("session %q not found in session db (InitSessionDB not called?)", sessionID)
	}

	// 初始化system prompt，只做一次！后续多轮用户输入不再重复加system；
	// 技能索引随启动时加载一次并注入，会话内不刷新。system prompt 不落盘，
	// 每次启动重建，续聊时模板/技能变更即时生效
	sysPrompt := laxctx.BuildSysPrompt(f.WorkingDir, laxctx.LoadSkills(f.WorkingDir), env.IsPlanMode, sessionID)
	if len(sess.Messages) == 0 || sess.Messages[0].Role != schema.RoleSystem {
		sess.View(sysPrompt)
	}

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

		err := f.Run(ctx, sess)
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

func (f *AgentEngine) Run(ctx context.Context, sess *Session) error {
	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 50 {
			return errTooManyTurns
		}

		// compress
		msgs, compressRes := laxctx.SimpleCompactor.Compress(sess.Messages, env.MaxWinToken, sess.TokenUsed)
		sess.Messages = msgs
		if compressRes != nil && compressRes.Total() > 0 {
			sess.WindowToken.TokenInput -= compressRes.InputTokenCompressed
			sess.WindowToken.TokenOutput -= compressRes.OutputTokenCompressed
			fmt.Printf("\033[33m[context compressed result] %d input tokens, %d output tokens\033[0m\n", compressRes.InputTokenCompressed, compressRes.OutputTokenCompressed)
		}

		// 历史唯一真相源是 session：Generate 前每轮重拼视图（system + 已有
		// 历史 + 本轮新消息），产生的消息一律经 Append 写回，无 slice 别名回写
		msgs, err := f.Provider.Generate(ctx, sess.Messages, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return fmt.Errorf("generating message: %w", err)
		}

		var toolCallCnt int
		for _, msg := range msgs {
			if msg.ReasoningContent != "" {
				fmt.Printf("\033[90m[LaxCode] thinking: %s\033[0m\n", msg.ReasoningContent)
			}
			if len(msg.Content) > 0 {
				fmt.Printf("\033[32m[LaxCode] LLM generates: %s\033[0m\n", msg.Content)
			}

			toolCallCnt += len(msg.ToolCalls)

			sess.Append(msg)

			for _, toolCall := range msg.ToolCalls {
				toolResult := f.ToolRegistry.Execute(ctx, &toolCall)
				content := toolResult.Output
				if toolResult.Error != nil {
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("error executing tool %s: %s", toolCall.Name, toolResult.Error))
					// 错误携带指引提示词时附到工具返回末尾，引导模型按 suggestion 修正
					var promptErr laxctx.ErrorWithPrompt
					if errors.As(toolResult.Error, &promptErr) {
						if prompt, ok := promptErr.AsPrompt(); ok {
							sb.WriteString("\n")
							sb.WriteString(prompt)
						}
					}
					if len(toolResult.Output) > 0 {
						sb.WriteString("工具的其他输出内容:\n")
						sb.WriteString(toolResult.Output)
					}
					content = sb.String()
				}
				sess.Append(schema.Message{
					Role:       schema.RoleUser,
					Content:    content,
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

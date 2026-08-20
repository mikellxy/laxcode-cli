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
			if errors.Is(err, errors.New("too many turns")) {
				fmt.Printf("[warn] %v\n", err)
			} else {
				return fmt.Errorf("run llm tool loop: %w", err)
			}
		}
	}
}

func (f *AgentEngine) Run(ctx context.Context) error {
	contextHis := f.contextHis

	turnCnt := 0
	prevTurnHasReadOrBash := false

	for {
		turnCnt++
		if turnCnt > 50 {
			return errors.New("too many turns")
		}
		msg, err := f.Provider.Generate(ctx, contextHis, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return fmt.Errorf("generating message: %w", err)
		}

		if len(msg.Content) > 0 {
			fmt.Printf("\033[32m[LaxCode] LLM generates: %s\033[0m\n", msg.Content)
		}

		if len(msg.ToolCalls) == 0 {
			return nil
		}

		contextHis = append(contextHis, *msg)

		// 统计本轮 read_file / bash 工具调用，并执行全部工具
		readBashCnt := 0
		for _, toolCall := range msg.ToolCalls {
			if toolCall.Name == "read_file" || toolCall.Name == "bash" {
				readBashCnt++
			}
			toolResult := f.ToolRegistry.Execute(ctx, &toolCall)
			contextHis = append(contextHis, schema.Message{
				Role:       schema.RoleUser,
				Content:    toolResult.Output,
				ToolCallID: toolResult.ToolCallID,
			})
		}

		// 记录并判断是否需要触发一轮“深度思考规划”：
		// 1. 连续两次外层 for 循环都发起了 read_file/bash 调用
		// 2. 本次一次发起了多个工具调用，且其中至少两个是 read_file/bash
		currTurnHasReadOrBash := readBashCnt > 0
		needDeepThink := (prevTurnHasReadOrBash && currTurnHasReadOrBash) || readBashCnt >= 2
		prevTurnHasReadOrBash = currTurnHasReadOrBash

		if !needDeepThink {
			continue
		}

		// 不带 available tools 发起一次生成请求，引导 LLM 根据新收集到的信息进行一轮深度思考规划
		deepMsg, err := f.Provider.Generate(ctx, contextHis, nil)
		if err != nil {
			return fmt.Errorf("generating deep thinking message: %w", err)
		}
		if len(deepMsg.Content) > 0 {
			fmt.Printf("\033[33m[LaxCode] LLM deep thinking: %s\033[0m\n", deepMsg.Content)
		}
		//contextHis = append(contextHis, *deepMsg)
	}
}

package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/config"
	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/printer"
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
	WorkDir      string
	PlanMode     bool
	// Session 是本引擎持有的会话：主 Agent 由 main 在启动期装配
	// （GetSession），子 Agent 在 Execute 时按需新建。Run 直接使用，
	// 不再逐轮传参。
	Session *Session
	// Printer 是本引擎的输出实例：主 Agent 默认取包级默认实例
	// （交互模式即 stdout + 主配色），子 Agent 用 WithColors 派生紫色
	// 实例；one-shot 模式由 main 先 SetDefault(Discard/stderr) 再装配，
	// 引擎随之静默。Run 与 TerminalLoop 的全部打印经它。
	Printer printer.Printer
}

func NewAgentEngine(toolRegistry tools.Registry, provider provider.Provider, workDir string, planMode bool, sess *Session) *AgentEngine {
	return &AgentEngine{
		ToolRegistry: toolRegistry,
		Provider:     provider,
		WorkDir:      workDir,
		PlanMode:     planMode,
		Session:      sess,
		Printer:      printer.Default(),
	}
}

func TerminalLoop(ctx context.Context, agentEngine *AgentEngine) error {
	sess := agentEngine.Session
	prn := agentEngine.Printer

	scanner := bufio.NewScanner(os.Stdin)
	prn.Printf(">>> Agent ready, input your question, input exit to quit\n")

	for {
		prn.Printf("%s> %s", printer.ColorBlue, printer.ColorReset)
		if !scanner.Scan() {
			// EOF
			prn.Printf("\nreceive EOF, exit\n")
			return nil
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}
		if strings.ToLower(userInput) == "exit" {
			prn.Printf("quit agent\n")
			return nil
		}

		// user prompy
		sess.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: userInput,
		})

		if _, err := agentEngine.Run(ctx); err != nil {
			// warn too many turns
			if errors.Is(err, errTooManyTurns) {
				prn.Printf("[warn] %v\n", err)
			} else {
				return fmt.Errorf("run llm tool loop: %w", err)
			}
		}
	}
}

// Run 执行一轮"生成-工具"循环直到模型给出无工具调用的最终回答，
// 返回该回答的文本内容。子 Agent 直接以返回值拿结果--
// 经无缓冲 channel 回传会在"发送等待接收、接收等待函数返回"间自死锁。
func (f *AgentEngine) Run(ctx context.Context) (string, error) {
	sess := f.Session
	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 50 {
			return "", errTooManyTurns
		}

		// compress
		msgs, compressRes := laxctx.SimpleCompactor.Compress(sess.Messages, config.MaxWinToken, sess.TokenUsed)
		sess.Messages = msgs
		if compressRes != nil && compressRes.Total() > 0 {
			sess.WindowToken.TokenInput -= compressRes.InputTokenCompressed
			sess.WindowToken.TokenOutput -= compressRes.OutputTokenCompressed
			f.Printer.PrintCompressResult(compressRes.InputTokenCompressed, compressRes.OutputTokenCompressed)
		}

		// 历史唯一真相源是 session：Generate 前每轮重拼视图（system + 已有
		// 历史 + 本轮新消息），产生的消息一律经 Append 写回，无 slice 别名回写
		msg, err := f.Provider.Generate(ctx, sess.Messages, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			return "", fmt.Errorf("generating message: %w", err)
		}

		toolCallCnt := len(msg.ToolCalls)
		f.Printer.PrintLLM(msg)

		sess.Append(*msg)

		// 并行策略见 executeToolCalls：多 read_file 调用 goroutine+channel
		// fork-join 并发执行，其余顺序执行；结果按原始调用顺序归位。
		results := f.executeToolCalls(ctx, msg.ToolCalls)
		for i, toolResult := range results {
			sess.Append(schema.Message{
				Role:       schema.RoleUser,
				Content:    buildToolResultContent(msg.ToolCalls[i].Name, toolResult),
				ToolCallID: toolResult.ToolCallID,
			})
		}
		if toolCallCnt == 0 {
			return msg.Content, nil
		}
	}
}

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
	"github.com/mikellxy/laxcode/internal/tracing"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	// Tracer 是本引擎的追踪注入点：Run/TerminalLoop 的全部 span 经它
	// 创建；nil 经构造器缺省为 noop，不产生任何观测输出。子 Agent
	// 继承父引擎的同一 Tracer，使子调用树留在同一条 trace 中。
	Tracer trace.Tracer
	// Role 标记 Agent 角色，写入 agent-run span 的 agent_role 属性：
	// 主 Agent 由构造器置 main；SubAgent 建子引擎后改置 sub。
	Role string
}

func NewAgentEngine(toolRegistry tools.Registry, provider provider.Provider, workDir string, planMode bool, sess *Session, tracer trace.Tracer) *AgentEngine {
	return &AgentEngine{
		ToolRegistry: toolRegistry,
		Provider:     provider,
		WorkDir:      workDir,
		PlanMode:     planMode,
		Session:      sess,
		Printer:      printer.Default(),
		Tracer:       tracing.OrNoop(tracer),
		Role:         tracing.AgentRoleMain,
	}
}

func TerminalLoop(ctx context.Context, agentEngine *AgentEngine) error {
	sess := agentEngine.Session
	prn := agentEngine.Printer
	// taskSeq 标记本次启动以来第几轮用户输入，写入 terminal-task span
	taskSeq := 0

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

		// 每次用户输入开启一条新 trace：terminal-task 为 root，记录
		// 完成该次输入任务的耗时与 token 合计（Run 前后 session 累计
		// 用量的差值）
		taskSeq++
		taskCtx, taskSpan := agentEngine.Tracer.Start(ctx, tracing.SpanTerminalTask,
			trace.WithAttributes(
				tracing.AttrSessionID.String(sess.ID()),
				tracing.AttrTaskSeq.Int(taskSeq),
			))
		tokenBefore := sess.TokenUsed

		// user prompy
		sess.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: userInput,
		})

		_, runErr := agentEngine.Run(taskCtx)
		taskSpan.SetAttributes(
			tracing.AttrInputTokens.Int(sess.TokenUsed.TokenInput-tokenBefore.TokenInput),
			tracing.AttrOutputTokens.Int(sess.TokenUsed.TokenOutput-tokenBefore.TokenOutput),
		)
		if runErr != nil {
			// warn too many turns
			taskSpan.RecordError(runErr)
			taskSpan.SetStatus(codes.Error, runErr.Error())
			taskSpan.End()
			if errors.Is(runErr, errTooManyTurns) {
				prn.Printf("[warn] %v\n", runErr)
			} else {
				return fmt.Errorf("run llm tool loop: %w", runErr)
			}
		} else {
			taskSpan.End()
		}
	}
}

// Run 执行一轮"生成-工具"循环直到模型给出无工具调用的最终回答，
// 返回该回答的文本内容。子 Agent 直接以返回值拿结果--
// 经无缓冲 channel 回传会在"发送等待接收、接收等待函数返回"间自死锁。
func (f *AgentEngine) Run(ctx context.Context) (string, error) {
	sess := f.Session
	// session_id 写入 ctx 向下传播：Registry 的 tool-exec span 经它读取
	// 业务关联键（span 属性不会自动继承）；子 Agent 的 Run 以子 session
	// id 覆盖。agent-run 的父链由调用方 ctx 决定——交互模式是
	// terminal-task，one-shot 为空 ctx，本 span 自动成为 root。
	ctx = tracing.ContextWithSessionID(ctx, sess.ID())
	ctx, runSpan := f.Tracer.Start(ctx, tracing.SpanAgentRun,
		trace.WithAttributes(
			tracing.AttrSessionID.String(sess.ID()),
			tracing.AttrAgentRole.String(f.Role),
		))
	// run 级 token 合计在 defer 中统一落属性，各 return 路径共享
	var runInput, runOutput int
	defer func() {
		runSpan.SetAttributes(
			tracing.AttrInputTokens.Int(runInput),
			tracing.AttrOutputTokens.Int(runOutput),
		)
		runSpan.End()
	}()
	turnCnt := 0

	for {
		turnCnt++
		if turnCnt > 50 {
			runSpan.RecordError(errTooManyTurns)
			runSpan.SetStatus(codes.Error, errTooManyTurns.Error())
			return "", errTooManyTurns
		}

		// 每轮外层 for 循环一个 react-loop span，序号即 turnCnt
		loopCtx, loopSpan := f.Tracer.Start(ctx, tracing.SpanReactLoop,
			trace.WithAttributes(tracing.AttrLoopSeq.Int(turnCnt)))

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
		genCtx, genSpan := f.Tracer.Start(loopCtx, tracing.SpanLLMGenerate)
		msg, err := f.Provider.Generate(genCtx, sess.Messages, f.ToolRegistry.GetAvailableTools())
		if err != nil {
			err = fmt.Errorf("generating message: %w", err)
			genSpan.RecordError(err)
			genSpan.SetStatus(codes.Error, err.Error())
			genSpan.End()
			loopSpan.End()
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, err.Error())
			return "", err
		}
		genSpan.SetAttributes(
			tracing.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
			tracing.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
			tracing.AttrToolCallCount.Int(len(msg.ToolCalls)),
		)
		genSpan.End()

		// loop 级 token 即本轮 generate 用量；run 级逐轮累计
		runInput += msg.TokenUsed.TokenInput
		runOutput += msg.TokenUsed.TokenOutput
		loopSpan.SetAttributes(
			tracing.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
			tracing.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
		)

		toolCallCnt := len(msg.ToolCalls)
		f.Printer.PrintLLM(msg)

		sess.Append(*msg)

		// 并行策略见 executeToolCalls：多 read_file 调用 goroutine+channel
		// fork-join 并发执行，其余顺序执行；结果按原始调用顺序归位。
		// loopCtx 携带 react-loop span，工具 span 经 Registry 挂到其下。
		results := f.executeToolCalls(loopCtx, msg.ToolCalls)
		for i, toolResult := range results {
			sess.Append(schema.Message{
				Role:       schema.RoleUser,
				Content:    buildToolResultContent(msg.ToolCalls[i].Name, toolResult),
				ToolCallID: toolResult.ToolCallID,
			})
		}
		loopSpan.End()
		if toolCallCnt == 0 {
			return msg.Content, nil
		}
	}
}

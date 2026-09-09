package run_cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
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

// fatal 只用于 TUI 接管终端之前的启动期错误：此时 stdout/stderr 仍是普通模式，
// 直接写 stderr 并 os.Exit 安全。TUI 启动后严禁再 os.Exit，否则 bubbletea 无法
// 恢复终端（会留下 raw 模式），故运行期错误一律经 inChan 回流到 TUI 呈现。
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// newEventConsumer 将流式边界和增量转换为 TUI 文本；每段只输出一次前缀。
func newEventConsumer(sendIn func(string)) func(*reactservice.ReactEvent) {
	return func(e *reactservice.ReactEvent) {
		switch e.Type {
		case reactservice.ReActEventTypeChunk:
			chunk := e.ChunkEvent
			if chunk == nil {
				return
			}
			switch chunk.Kind {
			case sharedkernel.ChunkReasoningStart:
				sendIn(ColorGray + "[LaxCode] thinking: ")
			case sharedkernel.ChunkTextStart:
				sendIn(ColorGreen + "[LaxCode] LLM generates: ")
			case sharedkernel.ChunkReasoningDelta, sharedkernel.ChunkTextDelta:
				sendIn(chunk.Delta)
			case sharedkernel.ChunkReasoningEnd, sharedkernel.ChunkTextEnd:
				sendIn(ColorReset + "\n")
			case sharedkernel.ChunkToolCall:
				// 参数已就绪；执行提示由后续 tool_call 事件在执行前显示。
			}
		case reactservice.ReActEventTypeToolCall:
			sendIn(fmt.Sprintf("%s[LaxCode] tool execute... %s%s\n", ColorYellow, e.Content, ColorReset))
		}
	}
}

func Run() {
	if err := checkConfig(); err != nil {
		fatal(err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	// ctx 可取消：ctrl+c 让 TUI 退出后立即 cancel，驱动消费 goroutine 与在途 Chat
	// 从通道收发 / LLM 调用中解阻塞收敛，避免 goroutine 永久悬挂（防死锁）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 用户输入经 outChan 上行，对端事件 / 回复经 inChan 下行。均为无缓冲通道，靠
	// 收发双方 rendezvous 同步；两侧的阻塞收发都用 select{ctx.Done()} 兜底。
	outChan := make(chan string)
	inChan := make(chan string)

	// sendIn 把一段内容写入 inChan；ctx 取消（TUI 已退出）时立即放弃，否则会因
	// 无人消费而永久阻塞。
	sendIn := func(s string) {
		select {
		case inChan <- s:
		case <-ctx.Done():
		}
	}

	// rcf：交互模式的事件呈现——把 ReAct 中间过程格式化后经 inChan 回流给 TUI 增量
	// 渲染（替代原先直接打印 stdout）。作为 Consumer 注入装配，与 one-shot 的静默
	// 丢弃回调形成对照。
	rcf := newEventConsumer(sendIn)

	// 装配（会话 / tracer / 工具集含子 Agent / provider / ReActService）收口到
	// cmd/agentasm 组合根，与 one-shot 共用。Cleanup 幂等（sync.Once），defer 一次。
	assembled, err := agentasm.Assemble(ctx, agentasm.Input{
		WorkDir:   workDir,
		SessionID: config.CliConf.Session,
		PlanMode:  config.CliConf.Plan,
		Consumer:  rcf,
	})
	if err != nil {
		fatal(err)
	}
	defer assembled.Cleanup()

	// 会话标识与就绪提示在 TUI 接管终端前打印，固定显示在交互区上方。
	fmt.Printf("session_id: %s\n", assembled.Session.ID)
	fmt.Printf(">>> Agent ready, input your question\n")

	// 消费 goroutine：outChan 取用户输入 → 调 Chat（其间 rcf 把事件写入 inChan）→
	// Chat 返回后写入终止符 StreamEnd，通知 TUI 结束本轮、进入下一轮用户输入。
	// ctx 取消即退出，避免 TUI 退出后仍阻塞在 outChan / inChan 上。
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case input := <-outChan:
				if _, err := assembled.Service.Chat(ctx, input); err != nil {
					// 运行期错误经 inChan 回流到 TUI 呈现，本轮仍以终止符收尾
					sendIn(fmt.Sprintf("%s\n%s[LaxCode] error: %v%s\n", ColorReset, ColorRed, err, ColorReset))
				}
				sendIn(cliprinter.StreamEnd)
			}
		}
	}()

	// 启动 TUI（阻塞）。用户 ctrl+c / SIGINT / SIGTERM 都会让 Run 返回，且终端已由
	// bubbletea 恢复正常模式。随后显式 cancel() 让上面的消费 goroutine 与在途 Chat
	// 先行收敛（早于 deferred Cleanup 回收工具 / tracer），最后 Run 返回、进程退出。
	if err := cliprinter.NewTUI(outChan, inChan).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "运行出错:", err)
	}
	cancel()
}

// Package main 是 LaxCode sse server 模式（cmd/run_sse）的示例客户端。
//
// 它向 POST /chat 发送 {"session_id","task"}，读取 text/event-stream 响应，按事件名
// （start / reasoning / message / tool_call / done / error）分流处理并流式打印。仅用
// 标准库，可直接 go run：
//
//	go run ./examples/sse-client -task "列出当前目录" -addr http://127.0.0.1:8080
//
// 客户端按 SSE 协议契约自行定义载荷结构（与 server 端 cmd/run_sse/sse.go 对齐），
// 不 import server 内部类型——这正是真实第三方客户端的接入方式。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
)

// ANSI 颜色，与 run_cli 的终端呈现保持一致：思考灰、正文绿、工具黄、生命周期蓝。
const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
)

// 以下载荷结构与 server 端 cmd/run_sse/sse.go 的事件 data 一一对齐。
type tokenStat struct {
	TokenInput  int `json:"token_input"`
	TokenOutput int `json:"token_output"`
}

type startData struct {
	SessionID string `json:"session_id"`
}

type deltaData struct {
	Delta string `json:"delta"`
}

type toolCallData struct {
	Info string `json:"info"`
}

type doneData struct {
	SessionID   string    `json:"session_id"`
	Result      string    `json:"result"`
	TokenUsed   tokenStat `json:"token_used"`
	WindowToken tokenStat `json:"window_token"`
}

type errorData struct {
	Message string `json:"message"`
}

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "LaxCode sse server base address")
	sessionID := flag.String("session", "", "session id to resume; empty starts a new session")
	task := flag.String("task", "", "task prompt (required)")
	flag.Parse()

	if strings.TrimSpace(*task) == "" {
		fmt.Fprintln(os.Stderr, "usage: sse-client -task=<prompt> [-session=<id>] [-addr=<url>]")
		os.Exit(2)
	}

	// ctx 由 SIGINT（Ctrl+C）取消，中断在途的流式读取。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *addr, *sessionID, *task); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

// run 构造并发送 POST /chat，校验响应后把 SSE 流交给 consumeSSE 解析、printer 呈现。
func run(ctx context.Context, addr, sessionID, task string) error {
	payload, err := json.Marshal(map[string]string{"session_id": sessionID, "task": task})
	if err != nil {
		return err
	}
	url := strings.TrimRight(addr, "/") + "/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	// 用 DefaultClient（无 Timeout）：SSE 是长连接，取消由 ctx 控制，不能设客户端超时。
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 非 200：server 在流开始之前用普通 JSON + 状态码报用法错误（400/409/500）。
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, readBody(resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		return fmt.Errorf("unexpected content-type %q (want text/event-stream)", ct)
	}

	p := &printer{}
	return consumeSSE(ctx, resp.Body, p.handle)
}

// consumeSSE 按 SSE 线协议逐帧解析 r：帧以空行结束，帧内 "event:" 定事件名、
// "data:" 定载荷（多行 data 以 \n 拼接），以 ":" 开头的注释行（keep-alive）跳过。
// 每解析出完整一帧即调用 onFrame(event, data)；onFrame 返回错误或 ctx 取消时中止。
// 解析与处理分离，使本函数可脱离网络单测（见 main_test.go）。
func consumeSSE(ctx context.Context, r io.Reader, onFrame func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	// done 帧的 data 是含完整回答的单行 JSON，可能远超 Scanner 默认 64KB 上限，放宽到 16MB。
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var event string
	var data strings.Builder

	// dispatch 结算当前帧：取出 event/data 并清空，非空则回调 onFrame。
	dispatch := func() error {
		name, payload := event, data.String()
		event = ""
		data.Reset()
		if name == "" && payload == "" {
			return nil
		}
		return onFrame(name, payload)
	}

	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Text()
		switch {
		case line == "": // 空行：一帧结束
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"): // 注释 / keep-alive，跳过
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ") // 规范：冒号后一个空格可选
			switch field {
			case "event":
				event = value
			case "data":
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
	}
	if err := dispatch(); err != nil { // 冲刷末尾残帧（流未以空行结束时）
		return err
	}
	return sc.Err()
}

// printer 按事件名分流打印，并跟踪 reasoning/message 段落状态：每段增量的前缀只打
// 一次，段落切换（工具调用 / 一轮结束）后重置，使多轮 ReAct 的每段都重新起头。
type printer struct {
	inReasoning bool
	inMessage   bool
}

func (p *printer) handle(event, data string) error {
	switch event {
	case "start":
		var d startData
		_ = json.Unmarshal([]byte(data), &d)
		fmt.Printf("\n%s[session]%s %s\n", colorBlue, colorReset, d.SessionID)
		p.inReasoning, p.inMessage = false, false
	case "reasoning":
		var d deltaData
		_ = json.Unmarshal([]byte(data), &d)
		if !p.inReasoning {
			fmt.Printf("\n%s[thinking]%s ", colorGray, colorReset)
			p.inReasoning, p.inMessage = true, false
		}
		fmt.Print(colorGray + d.Delta + colorReset)
	case "message":
		var d deltaData
		_ = json.Unmarshal([]byte(data), &d)
		if !p.inMessage {
			fmt.Printf("\n%s[answer]%s ", colorGreen, colorReset)
			p.inReasoning, p.inMessage = false, true
		}
		fmt.Print(colorGreen + d.Delta + colorReset)
	case "tool_call":
		var d toolCallData
		_ = json.Unmarshal([]byte(data), &d)
		fmt.Printf("\n%s[tool]%s %s\n", colorYellow, colorReset, d.Info)
		p.inReasoning, p.inMessage = false, false
	case "done":
		var d doneData
		_ = json.Unmarshal([]byte(data), &d)
		fmt.Printf("\n%s[done]%s tokens in/out = %d/%d\n",
			colorBlue, colorReset, d.TokenUsed.TokenInput, d.TokenUsed.TokenOutput)
		p.inReasoning, p.inMessage = false, false
	case "error":
		var d errorData
		_ = json.Unmarshal([]byte(data), &d)
		return fmt.Errorf("server error: %s", d.Message)
	default: // 未知事件原样打印，便于协议演进时调试
		fmt.Printf("\n[%s] %s\n", event, data)
	}
	return nil
}

// readBody 读取（限长）响应体用于错误提示，忽略读取错误。
func readBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(b))
}

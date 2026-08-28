// Package printer 是人类可读输出的唯一收口：模型消息、工具调用提示、压缩提示、
// 警告与调试信息全部经 Printer 实例输出。输出行为由注入的实例决定——
// WriterPrinter 写往指定目的地（stdout/stderr 等，配色可注入），DiscardPrinter
// 全静默（one-shot 模式默认）。AgentEngine 与工具注册表持有注入的实例；
// 无明确宿主的散点（session/skill 警告、调试、横幅）走包级默认实例，
// SetDefault 一次替换即可让全部输出落在同一行为上（one-shot 的静默/verbose 闸门）。
// 本包是叶子包：仅依赖 internal/schema 与标准库，不产生 import 环。
package printer

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/mikellxy/laxcode/internal/schema"
)

// ANSI 颜色控制符，沿用收口前 engine/printer.go 的配色约定：
// 主 Agent thinking 灰、正文绿；子 Agent 紫；工具/警告黄；严重警告红；
// 调试灰；REPL 提示符蓝。
const (
	ColorReset  = "\033[0m"
	ColorGray   = "\033[90m"
	ColorGreen  = "\033[32m"
	ColorPurple = "\033[35m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorBlue   = "\033[34m"
)

// Printer 是输出行为抽象：engine 与工具注册表持有的实例决定中间过程
// 输出到哪里、以什么配色。WithColors 从当前实例派生仅更换配色的子实例，
// 目的地与并发保护继承父实例——子 Agent 换色不换地方，one-shot 下子
// Agent 自动继承静默/stderr。
type Printer interface {
	// Printf 原样输出（无颜色无前缀），供 REPL 交互提示、启动横幅等使用
	Printf(format string, args ...any)
	// PrintLLM 打印 assistant 消息（thinking + 正文），配色取实例配置
	PrintLLM(msg *schema.Message)
	// PrintToolCall 打印工具执行前提示（黄色）
	PrintToolCall(info string)
	// PrintCompressResult 打印上下文压缩结果（黄色）
	PrintCompressResult(inputTok, outputTok int)
	// WithColors 派生同目的地、指定配色的子实例
	WithColors(thinkColor, contentColor string) Printer
}

// WriterPrinter 把输出写到指定 io.Writer，thinking/正文配色可注入；
// 多个实例共享写锁（WithColors 派生的子实例与父实例共用），写操作串行化，
// 并发打印（如并行 read_file 的调用提示）不会行内交错。
type WriterPrinter struct {
	w            io.Writer
	mu           *sync.Mutex
	thinkColor   string
	contentColor string
}

// NewWriterPrinter 以指定目的地与配色构造实例。
func NewWriterPrinter(w io.Writer, thinkColor, contentColor string) *WriterPrinter {
	return &WriterPrinter{w: w, mu: &sync.Mutex{}, thinkColor: thinkColor, contentColor: contentColor}
}

// NewMainPrinter 是交互模式默认实例：stdout + 主 Agent 配色（thinking 灰、正文绿）。
func NewMainPrinter() *WriterPrinter {
	return NewWriterPrinter(os.Stdout, ColorGray, ColorGreen)
}

// NewSubPrinter 构造子 Agent 配色实例（thinking 与正文统一紫色）。
func NewSubPrinter(w io.Writer) *WriterPrinter {
	return NewWriterPrinter(w, ColorPurple, ColorPurple)
}

// Printf 持锁一次写完，保证单次输出原子、并发不交错。
func (p *WriterPrinter) Printf(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.w, format, args...)
}

// PrintLLM 打印 assistant 消息：整条消息（thinking + 正文）拼装为一次写，
// 避免并发下两部分之间插入其他输出。
func (p *WriterPrinter) PrintLLM(msg *schema.Message) {
	var sb strings.Builder
	if msg.ReasoningContent != "" {
		fmt.Fprintf(&sb, "%s[LaxCode] thinking: %s%s\n", p.thinkColor, msg.ReasoningContent, ColorReset)
	}
	if len(msg.Content) > 0 {
		fmt.Fprintf(&sb, "%s[LaxCode] LLM generates: %s%s\n", p.contentColor, msg.Content, ColorReset)
	}
	if sb.Len() > 0 {
		p.Printf("%s", sb.String())
	}
}

// PrintToolCall 打印工具执行前提示（黄色）。
func (p *WriterPrinter) PrintToolCall(info string) {
	p.Printf("%s[LaxCode] tool execute... %s%s\n", ColorYellow, info, ColorReset)
}

// PrintCompressResult 打印上下文压缩结果（黄色）。
func (p *WriterPrinter) PrintCompressResult(inputTok, outputTok int) {
	p.Printf("%s[context compressed result] %d input tokens, %d output tokens%s\n",
		ColorYellow, inputTok, outputTok, ColorReset)
}

// WithColors 派生同目的地、指定配色的子实例，写锁与父实例共享。
func (p *WriterPrinter) WithColors(thinkColor, contentColor string) Printer {
	return &WriterPrinter{w: p.w, mu: p.mu, thinkColor: thinkColor, contentColor: contentColor}
}

// DiscardPrinter 全静默实现：所有输出为空操作，one-shot 模式默认实例。
type DiscardPrinter struct{}

func (DiscardPrinter) Printf(string, ...any)             {}
func (DiscardPrinter) PrintLLM(*schema.Message)          {}
func (DiscardPrinter) PrintToolCall(string)              {}
func (DiscardPrinter) PrintCompressResult(int, int)      {}
func (DiscardPrinter) WithColors(string, string) Printer { return DiscardPrinter{} }

var (
	defaultMu      sync.RWMutex
	defaultPrinter Printer = NewMainPrinter()
)

// SetDefault 替换包级默认实例，随后经 Default() 取实例的 engine/registry
// 与包级委托函数全部落在同一行为上。one-shot 模式在装配引擎前调用一次：
// 默认 DiscardPrinter{}，-verbose 时 NewWriterPrinter(os.Stderr, ...)。
func SetDefault(p Printer) {
	defaultMu.Lock()
	defaultPrinter = p
	defaultMu.Unlock()
}

// Default 返回包级默认实例。
func Default() Printer {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultPrinter
}

// Printf 包级委托：经默认实例原样输出（横幅等无宿主散点）。
func Printf(format string, args ...any) {
	Default().Printf(format, args...)
}

// Warnf 输出黄色警告（[LaxCode] 前缀）：可跳过的局部异常，如历史坏行、
// 无效 skill、快照写失败。经默认实例落笔。
func Warnf(format string, args ...any) {
	Default().Printf(ColorYellow+"[LaxCode] "+format+ColorReset+"\n", args...)
}

// Errorf 输出红色 WARN 警告（[LaxCode][WARN] 前缀）：数据可能丢失的
// 显眼警告，如会话历史写盘失败。经默认实例落笔。
func Errorf(format string, args ...any) {
	Default().Printf(ColorRed+"[LaxCode][WARN] "+format+ColorReset+"\n", args...)
}

// Debugf 输出灰色调试信息（[debug] 前缀）。是否开启由调用方判定
// （config.Debugf 检查开关后委托到此），本函数不做开关判断。
func Debugf(format string, args ...any) {
	Default().Printf(ColorGray+"[debug] "+format+ColorReset+"\n", args...)
}

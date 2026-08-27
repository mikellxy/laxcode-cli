package engine

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/schema"
)

// ANSI 颜色控制符：主 Agent thinking 用灰色、正文用绿色；
// 子 Agent 统一紫色，与主 Agent 输出在终端上一眼可分。
const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorGreen  = "\033[32m"
	colorPurple = "\033[35m"
)

// printLLMMessage 是 assistant 消息（thinking + 正文）的唯一打印出口，
// 配色由调用方注入，打印逻辑不感知主/子 Agent 之别。
func printLLMMessage(thinkColor, contentColor string, msg *schema.Message) {
	if msg.ReasoningContent != "" {
		fmt.Printf("%s[LaxCode] thinking: %s%s\n", thinkColor, msg.ReasoningContent, colorReset)
	}
	if len(msg.Content) > 0 {
		fmt.Printf("%s[LaxCode] LLM generates: %s%s\n", contentColor, msg.Content, colorReset)
	}
}

// PrintMainLLM 主 Agent 配色：thinking 灰、正文绿。
func PrintMainLLM(msg *schema.Message) {
	printLLMMessage(colorGray, colorGreen, msg)
}

// PrintSubLLM 子 Agent 配色：thinking 与正文统一紫色。
func PrintSubLLM(msg *schema.Message) {
	printLLMMessage(colorPurple, colorPurple, msg)
}

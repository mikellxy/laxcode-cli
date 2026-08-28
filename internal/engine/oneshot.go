package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mikellxy/laxcode/internal/printer"
	"github.com/mikellxy/laxcode/internal/schema"
)

// one-shot 结构化返回中 error.type 的机器可读取值。
const (
	// ErrTypeUsage 参数用法错误：发生在 session 建立之前，
	// 返回中 session_id 为空、token 统计为零值。
	ErrTypeUsage = "usage"
	// ErrTypeGenerate 模型调用失败。
	ErrTypeGenerate = "generate"
	// ErrTypeTooManyTurns 单轮工具循环达到上限：交互模式下是警告并继续，
	// one-shot 下为终态失败（session 已落盘，可凭 session_id 续跑）。
	ErrTypeTooManyTurns = "too_many_turns"
)

// OneShotError 是结构化返回 error 字段的载荷：type 机器可读、
// message 供调用方诊断（用法错误须能据此修正调用参数）。
type OneShotError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// OneShotResult 是 one-shot 模式向 stdout 输出的扁平同构结构：
// 成功与失败共用一套 schema（error 为 null 即成功），失败也带 token
// 统计——计费已发生，调用方有权知道；rounds 不在返回中。
type OneShotResult struct {
	SessionID   string                 `json:"session_id"`
	Result      string                 `json:"result"`
	TokenUsed   schema.TokenStatistics `json:"token_used"`
	WindowToken schema.TokenStatistics `json:"window_token"`
	Error       *OneShotError          `json:"error"`
}

// NewUsageResult 构造参数用法错误的结构化返回：发生在 session 建立之前，
// session_id 为空串、token 统计为零值。
func NewUsageResult(message string) OneShotResult {
	return OneShotResult{Error: &OneShotError{Type: ErrTypeUsage, Message: message}}
}

// OneShotLoop 与 TerminalLoop 平级的 one-shot 前端：把 task 作为 user 消息
// 追加进 session 后执行一次 Run，把结构化结果单行 JSON 直写 stdout。
// JSON 是契约出口，不经 Printer（printer 目的地可能是 stderr/discard），
// 与中间过程输出彻底分流。返回值仅供 main 映射 exit code：
// nil 成功，非 nil 为运行失败（JSON 内已带 error 细节）。
func OneShotLoop(ctx context.Context, agentEngine *AgentEngine, task string) error {
	sess := agentEngine.Session

	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: task,
	})

	result, runErr := agentEngine.Run(ctx)

	res := OneShotResult{
		SessionID:   sess.ID(),
		Result:      result,
		TokenUsed:   sess.TokenUsed,
		WindowToken: sess.WindowToken,
	}
	if runErr != nil {
		res.Error = &OneShotError{Type: oneShotErrType(runErr), Message: runErr.Error()}
	}

	WriteOneShotResult(os.Stdout, res)
	return runErr
}

// oneShotErrType 把 Run 的错误映射为机器可读的 error.type：
// 工具循环超限是已知终态，其余一律视为模型调用失败。
func oneShotErrType(err error) string {
	if errors.Is(err, errTooManyTurns) {
		return ErrTypeTooManyTurns
	}
	return ErrTypeGenerate
}

// WriteOneShotResult 把结果序列化为单行 JSON（带换行）写入 w。
func WriteOneShotResult(w io.Writer, res OneShotResult) {
	data, err := json.Marshal(res)
	if err != nil {
		// 字段全为可序列化类型，失败仅剩理论可能；经默认实例告警不中断
		printer.Errorf("marshal one-shot result failed: %v", err)
		return
	}
	fmt.Fprintln(w, string(data))
}

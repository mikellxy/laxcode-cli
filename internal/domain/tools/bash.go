package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	ToolBash           = "bash"
	ToolReadFile       = "read_file"
	ToolWriteFile      = "write_file"
	ToolEditFile       = "edit_file"
	ToolRunSubAgent    = "run_sub_agent"
	defaultBashTimeout = 30 * time.Second
)

type BashTool struct {
	WorkDir string
	// Timeout 是单条命令的超时上限，零值取默认 30s；测试可缩短
	Timeout time.Duration
	// Runner 是命令执行端口，经构造注入；进程组、输出临时文件与
	// 后台进程回收等 OS 机制见 infrastructure/shell
	Runner ShellRunner
}

func NewBashTool(workDir string, runner ShellRunner) *BashTool {
	return &BashTool{WorkDir: workDir, Runner: runner, Timeout: defaultBashTimeout}
}

func (b *BashTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (b *BashTool) BeforeExecInfo(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return ToolBash + "()"
	}
	command, ok := argsMap["command"]
	if !ok {
		return ToolBash + "()"
	}

	return fmt.Sprintf("%s(%s)", ToolBash, command)
}

func (b *BashTool) Name() string {
	return ToolBash
}

func (b *BashTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name: b.Name(),
		Description: "在工作目录执行 bash 命令。需要后台进程（如启动服务器）时，" +
			"务必重定向输出到日志文件并记录pid，例如: " +
			"python3 server.py > /tmp/srv.log 2>&1 & echo \"pid=$!\"，" +
			"之后用返回的pid执行 kill -9 <pid> 清理，也可 tail 日志文件排错",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "执行bash 命令，如 grep -rn NewAgentEngine",
				},
			},
			"required": []string{"command"},
		},
	}
}

type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Truncated bool   `json:"is_truncated"`
	Desc      string `json:"desc"`
}

func (e *ExecResult) String() string {
	s := "%s\nexit_code:%d\nstdout_truncated:%v\nstdout:%s"
	return fmt.Sprintf(s, e.Desc, e.ExitCode, e.Truncated, e.Stdout)
}

func (b *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {

	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", NewErrorWithPrompt(&ParamError{}, err)
	}
	command, ok := argsMap["command"]
	if !ok || strings.TrimSpace(command) == "" {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("command required"))
	}

	outcome, err := b.Runner.Run(ctx, b.WorkDir, command, b.timeout())
	if err != nil {
		// 超时/取消单独成文案：引导模型改用后台进程或缩小命令粒度，
		// 而非误判为命令本身写错
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", NewErrorWithPrompt(&BashExecuteError{},
				fmt.Errorf("bash执行超时或被取消: %w", err))
		}
		return "", NewErrorWithPrompt(&BashExecuteError{}, err)
	}

	// 非零退出不是工具错误：退出码与原始错误描述一并回给模型自行判断
	result := &ExecResult{Desc: "命令执行成功", Stdout: outcome.Output, ExitCode: outcome.ExitCode}
	if outcome.ExitErr != "" {
		result.Desc = "命令执行失败: " + outcome.ExitErr
	}

	const maxRune = 8000
	result.Stdout, result.Truncated = safeTruncateUTF8(result.Stdout, maxRune)
	if result.Truncated {
		result.Desc += " ;bash输出过长已截断至前:" + strconv.Itoa(maxRune) + "字符"
	}

	return result.String(), nil
}

// Close 委托命令执行端口回收本次运行内遗留的后台进程（含 LLM 遗忘
// 清理的）与输出临时文件，随会话结束由 Registry.Close 统一调用
func (b *BashTool) Close() error {
	return b.Runner.Close()
}

func (b *BashTool) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return defaultBashTimeout
}

func safeTruncateUTF8(s string, maxRune int) (out string, truncated bool) {
	r := []rune(s)
	if len(r) <= maxRune {
		return s, false
	}
	return string(r[:maxRune]), true
}

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/schema"
)

type BashTool struct {
	WorkDir string
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{WorkDir: workDir}
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

func (b *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        b.Name(),
		Description: "在工作目录执行 bash 命令",
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
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}
	command, ok := argsMap["command"]
	if !ok || strings.TrimSpace(command) == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("command required"))
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = b.WorkDir
	output, err := cmd.CombinedOutput()
	result := &ExecResult{Desc: "命令执行成功", Stdout: string(output)}

	if ctx.Err() != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{},
			fmt.Errorf("bash执行超时或被取消: %w", ctx.Err()))
	}

	if err != nil {
		result.Desc = "命令执行失败: " + err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{}, err)
		}
	}

	const maxRune = 8000
	result.Stdout, result.Truncated = safeTruncateUTF8(result.Stdout, maxRune)
	if result.Truncated {
		result.Desc += " ;bash输出过长已截断至前:" + strconv.Itoa(maxRune) + "字符"
	}

	return result.String(), nil
}

func safeTruncateUTF8(s string, maxRune int) (out string, truncated bool) {
	r := []rune(s)
	if len(r) <= maxRune {
		return s, false
	}
	return string(r[:maxRune]), true
}

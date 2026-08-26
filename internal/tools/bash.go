package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/env"
	"github.com/mikellxy/laxcode/internal/schema"
)

type BashTool struct{}

func (b BashTool) Info(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "bash()"
	}
	command, ok := argsMap["command"]
	if !ok {
		return "bash()"
	}

	return fmt.Sprintf("bash(%s)", command)
}

func (b BashTool) Name() string {
	return "bash"
}

func (b BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "bash",
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

func (b BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {

	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}
	command, ok := argsMap["command"]
	if !ok || strings.TrimSpace(command) == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("command required"))
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = env.WorkDir
	output, err := cmd.CombinedOutput()
	result := &ExecResult{Desc: "命令执行成功", Stdout: string(output)}
	if err != nil {
		result.Desc = "命令执行失败: " + err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}

	const maxLen = 8000
	if len(result.Stdout) > maxLen {
		result.Desc = "命令执行成功. bash输出过长以截断至前:" + strconv.Itoa(maxLen) + "字节"
		result.Truncated = true
		result.Stdout = result.Stdout[:maxLen]
	}

	return result.String(), nil
}

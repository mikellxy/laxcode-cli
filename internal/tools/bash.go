package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

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

func (b BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {

	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", err
	}
	command, ok := argsMap["command"]
	if !ok {
		return "", fmt.Errorf("command required")
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = env.WorkDir
	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	if err != nil {
		return fmt.Sprintf("执行报错: %s\nbash输出:%s\n", err.Error(), outputStr), nil
	}
	if len(outputStr) == 0 {
		return "命令执行成功，无bash输", nil
	}

	const maxLen = 8000
	if len(outputStr) > maxLen {
		return fmt.Sprintf("命令执行成功. bash输出过长以截断至前%d字节:\n%s", maxLen, outputStr[:maxLen]), nil
	}

	return fmt.Sprintf("命令执行成功, bash输出:\n%s", outputStr), nil
}

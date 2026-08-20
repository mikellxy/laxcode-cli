package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/env"
	"github.com/mikellxy/laxcode/internal/schema"
)

type ReadFileTool struct {
}

func (r ReadFileTool) Info(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "read_file()"
	}
	path, ok := argsMap["path"]
	if !ok {
		return "read_file()"
	}

	return fmt.Sprintf("read_file(%s)", path)
}

func (r ReadFileTool) Name() string {
	return "read_file"
}

func (r ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "read_file",
		Description: "读取文件内容。 **严格限制**只读取你的工作目录下的文件，提供文件在工作目录的相对路径",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要读的文件的相对路径，如 cmd/main/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", err
	}

	path, ok := argsMap["path"]
	if !ok {
		return "", fmt.Errorf("file path required")
	}

	path = filepath.Join(env.WorkDir, path)

	b, err := os.ReadFile(path)

	return string(b), err
}

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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.Writer(&stdout)
	cmd.Stderr = io.Writer(&stderr)

	if err := cmd.Run(); err != nil {
		return "", err
	}

	if errMsg := stderr.String(); errMsg != "" {
		return "", fmt.Errorf("%s", errMsg)
	}

	return stdout.String(), nil
}

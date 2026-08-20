package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

type WriteFileTool struct{}

func (w WriteFileTool) Info(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "write_file()"
	}
	path, ok := argsMap["path"]
	if !ok {
		return "write_file()"
	}

	return fmt.Sprintf("write_file(%s)", path)
}

func (w WriteFileTool) Name() string {
	return "write_file"
}

func (w WriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "write_file",
		Description: "写入完整文件内容，创建新文件或覆写已有文件。**严格限制**只写入你的工作目录下的文件，提供文件在工作目录的相对路径；若父目录不存在会自动创建",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要写入的文件的相对路径，如 cmd/main/main.go",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (w WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", err
	}

	path, ok := argsMap["path"]
	if !ok || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("file path required")
	}

	content, ok := argsMap["content"]
	if !ok {
		return "", fmt.Errorf("file content required")
	}

	target, err := safeJoinWorkDir(path)
	if err != nil {
		return "", err
	}

	// 自动创建父目录
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}

	// 返回相对工作目录的路径，便于模型确认
	rel, err := filepath.Rel(env.WorkDir, target)
	if err != nil {
		rel = target
	}

	return fmt.Sprintf("内容成功写入文件：%s", filepath.ToSlash(rel)), nil
}

// safeJoinWorkDir 将用户提供的相对路径安全地解析到工作目录内，
// 防止 ../ 路径穿越或绝对路径逃逸到工作目录之外。
func safeJoinWorkDir(rel string) (string, error) {
	workDirAbs, err := filepath.Abs(env.WorkDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}

	// 工具契约要求相对路径，显式拒绝绝对路径，避免语义歧义
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be a relative path within working directory %q", rel, workDirAbs)
	}

	target := filepath.Clean(filepath.Join(workDirAbs, rel))

	// 校验目标路径必须位于工作目录内
	check, err := filepath.Rel(workDirAbs, target)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	if check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes working directory %q", rel, workDirAbs)
	}

	return target, nil
}

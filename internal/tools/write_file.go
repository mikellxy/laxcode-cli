package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/schema"
)

type WriteFileTool struct {
	WorkDir string
}

func NewWriteFileTool(workDir string) *WriteFileTool {
	return &WriteFileTool{WorkDir: workDir}
}

func (w *WriteFileTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (w *WriteFileTool) BeforeExecInfo(args json.RawMessage) string {
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

func (w *WriteFileTool) Name() string {
	return "write_file"
}

func (w *WriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        w.Name(),
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

func (w *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}

	path, ok := argsMap["path"]
	if !ok || strings.TrimSpace(path) == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("path required"))
	}

	content, ok := argsMap["content"]
	if !ok {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("content required"))
	}

	target, err := safeJoinWorkDir(path, w.WorkDir)
	if err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.FilePathError{}, err)
	}

	// 自动创建父目录
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.FileIOError{}, fmt.Errorf("create parent dir: %w", err))
	}

	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.FileIOError{}, err)
	}

	// 返回相对工作目录的路径，便于模型确认
	rel, err := filepath.Rel(w.WorkDir, target)
	if err != nil {
		rel = target
	}

	return fmt.Sprintf("内容成功写入文件：%s", filepath.ToSlash(rel)), nil
}

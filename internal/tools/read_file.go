package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/schema"
	"github.com/mikellxy/laxcode/internal/utils"
)

type readFileToolArgs struct {
	Path        string `json:"path"`
	StartLineNo int    `json:"start_line_no"`
	StartBytes  int    `json:"start_bytes"`
}

const (
	readFileToolMaxReadLines = 2000
	readFileToolMaxReadBytes = 50 * 1024
)

type ReadFileTool struct {
	WorkDir string `json:"work_dir"`
}

func NewReadFileTool(workDir string) *ReadFileTool {
	return &ReadFileTool{WorkDir: workDir}
}

func (r *ReadFileTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (r *ReadFileTool) BeforeExecInfo(args json.RawMessage) string {
	var argsObj readFileToolArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return "read_file()"
	}
	if argsObj.Path == "" {
		return "read_file()"
	}

	return fmt.Sprintf("read_file(path=%s, start_line_no=%d, start_bytes=%d)", argsObj.Path, argsObj.StartLineNo, argsObj.StartBytes)
}

func (r *ReadFileTool) Name() string {
	return "read_file"
}

func (r *ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        r.Name(),
		Description: "读取文件内容。 **严格限制**只读取你的工作目录下的文件，提供文件在工作目录的相对路径。单次最多返回 2000 行且内容不超过 50KB，输出末尾以 (...) 标注是否读完、最后一行行号及续读参数，未读完时按标注的 start_line_no/start_bytes 续读",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要读的文件的相对路径，如 cmd/main/main.go",
				},
				"start_line_no": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "起始行号，1-based，最小为 1，禁止传 0；从头开始读传 start_line_no=1",
				},
				"start_bytes": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "起始行内字节偏移，1-based，最小为 1，禁止传 0；从起始字节开始读传 start_bytes=1",
				},
			},
			"required": []string{"path", "start_line_no", "start_bytes"},
		},
	}
}

func (r *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var argsObj readFileToolArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}

	if argsObj.Path == "" || strings.TrimSpace(argsObj.Path) == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("path required"))
	}

	pathSafe, err := safeJoinWorkDir(argsObj.Path, r.WorkDir)
	if err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.FilePathError{}, err)
	}

	result := utils.ReadUpToNKB(readFileToolMaxReadBytes, readFileToolMaxReadLines,
		argsObj.StartLineNo, argsObj.StartBytes, pathSafe)
	if result.Err != nil {
		if os.IsNotExist(result.Err) {
			return "", laxctx.NewErrorWithPrompt(&laxctx.FileNotExistError{},
				fmt.Errorf("文件 %s 不存在，请核对相对路径是否正确", argsObj.Path))
		}
		return "", laxctx.NewErrorWithPrompt(&laxctx.FileIOError{}, result.Err)
	}

	if len(result.Content) == 0 {
		return "(" + readFooter(result) + ")\n", nil
	}

	var sb strings.Builder
	sb.Write(result.Content)
	sb.WriteString("\n(")
	sb.WriteString(readFooter(result))
	sb.WriteString(")\n")
	return sb.String(), nil
}

// readFooter 生成 read_file 输出的尾部状态说明：文件是否读完、读到的
// 最后一行行号、最后一行是否截断及该行已读字节数，并给出续读参数，
// 使模型无需额外信息即可自行翻页。
func readFooter(res *utils.ReadResult) string {
	if res.EndLineNo == 0 {
		return "文件为空"
	}

	var b strings.Builder
	if res.Finished {
		b.WriteString("文件已读完")
	} else {
		b.WriteString("文件未读完")
	}
	fmt.Fprintf(&b, "，最后一行行号: %d", res.EndLineNo)

	switch {
	case res.LastLineTruncated:
		fmt.Fprintf(&b, "，该行未读完整(已读 %d 字节)，续读请传 start_line_no=%d, start_bytes=%d",
			res.LastLineTruncatedBytes, res.EndLineNo, res.LastLineTruncatedBytes+1)
	case !res.Finished:
		fmt.Fprintf(&b, "，续读请传 start_line_no=%d, start_bytes=1", res.EndLineNo+1)
	}

	if res.Finished && len(res.Content) == 0 {
		b.WriteString("（本次未读取到内容：起始行超出文件范围）")
	}
	return b.String()
}

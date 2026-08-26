package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mikellxy/laxcode/internal/env"
	"github.com/mikellxy/laxcode/internal/schema"
)

type EditFileTool struct{}

func (e EditFileTool) Info(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "edit_file()"
	}
	path, ok := argsMap["path"]
	if !ok {
		return "edit_file()"
	}

	return fmt.Sprintf("edit_file(%s)", path)
}

func (e EditFileTool) Name() string {
	return "edit_file"
}

func (e EditFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "edit_file",
		Description: "替换文件中已有的文本片段。old_text 必须与文件内容精确一致且在文件中唯一；若报错多处匹配请扩大 old_text 加入上下文行，若未匹配请重新 read_file 确认内容。文件必须已存在，新建文件请使用 write_file。**严格限制**只编辑工作目录内的文件，提供相对路径",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要编辑的文件的相对路径，如 cmd/main/main.go",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "要替换的原文片段，须与文件内容精确一致并包含足够上下文使其在文件中唯一",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "替换后的内容，允许为空字符串（删除该片段）",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

func (e EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", err
	}

	path, ok := argsMap["path"]
	if !ok || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("file path required")
	}
	oldText, ok := argsMap["old_text"]
	if !ok || strings.TrimSpace(oldText) == "" {
		return "", fmt.Errorf("old_text required")
	}
	newText, ok := argsMap["new_text"]
	if !ok {
		return "", fmt.Errorf("new_text required")
	}

	target, err := safeJoinWorkDir(path)
	if err != nil {
		return "", err
	}

	b, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件 %s 不存在，新建文件请使用 write_file", path)
		}
		return "", err
	}

	newContent, start, end, level, err := applyEdit(string(b), oldText, newText)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(target, []byte(newContent), 0o644); err != nil {
		return "", err
	}

	rel, err := filepath.Rel(env.WorkDir, target)
	if err != nil {
		rel = target
	}

	return fmt.Sprintf("已在 %s 第 %d-%d 行完成替换（%s）", filepath.ToSlash(rel), start, end, level), nil
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

// ---------- edit_file 四级匹配引擎 ----------

// edit_file 匹配层级标识，用于成功反馈。
const (
	editLevelExact = "精确匹配"
	editLevelNorm  = "换行归一化匹配"
	editLevelTrim  = "首尾空白容忍匹配"
	editLevelLines = "行级匹配"
)

// applyEdit 在 content 中按四级宽容降级策略定位 oldText 并替换为 newText，
// 返回替换后的完整内容、替换区间（1-based 起止行号）与命中层级：
//  1. 精确匹配：原始字节域，其余字节不动
//  2. 换行符归一化匹配：双侧 \r\n 归一为 \n，命中后全文件以 LF 写回
//  3. 首尾空白容忍匹配：oldText 去首尾空白后在归一化内容中定位，两侧空白保留
//  4. 行级匹配：双侧逐行去首尾空白后滑动窗口比较，newText 原样写入
//
// 任一层命中多处即报错并给出各行号；四层均未命中返回引导重新读取的错误。
// newText 的换行符统一按 LF 写入。调用方须保证 oldText 非空白。
func applyEdit(content, oldText, newText string) (string, int, int, string, error) {
	newText = normalizeNewlines(newText)

	// L1 精确匹配：原始字节域
	if hits := findAll(content, oldText); len(hits) > 0 {
		if len(hits) > 1 {
			return "", 0, 0, "", multiMatchError(offsetLines(content, hits))
		}
		i := hits[0]
		start := lineAt(content, i)
		return replaceAt(content, i, len(oldText), newText), start, lineAt(content, i+len(oldText)-1), editLevelExact, nil
	}

	norm := normalizeNewlines(content)
	normOld := normalizeNewlines(oldText)

	// L2 换行符归一化匹配：命中后全文件以 LF 写回
	if hits := findAll(norm, normOld); len(hits) > 0 {
		if len(hits) > 1 {
			return "", 0, 0, "", multiMatchError(offsetLines(norm, hits))
		}
		i := hits[0]
		start := lineAt(norm, i)
		return replaceAt(norm, i, len(normOld), newText), start, lineAt(norm, i+len(normOld)-1), editLevelNorm, nil
	}

	// L3 首尾空白容忍匹配：命中区间为去空白后内容的出现区间
	if trimmedOld := strings.TrimSpace(normOld); trimmedOld != "" {
		if hits := findAll(norm, trimmedOld); len(hits) > 0 {
			if len(hits) > 1 {
				return "", 0, 0, "", multiMatchError(offsetLines(norm, hits))
			}
			i := hits[0]
			start := lineAt(norm, i)
			return replaceAt(norm, i, len(trimmedOld), newText), start, lineAt(norm, i+len(trimmedOld)-1), editLevelTrim, nil
		}
	}

	// L4 行级匹配：双侧逐行去首尾空白后滑动窗口比较
	if strings.TrimSpace(normOld) != "" {
		oldLines := splitTrimLines(normOld)
		windows := matchLines(splitTrimLines(norm), oldLines)
		if len(windows) > 1 {
			lineNos := make([]int, len(windows))
			for i, w := range windows {
				lineNos[i] = w + 1
			}
			return "", 0, 0, "", multiMatchError(lineNos)
		}
		if len(windows) == 1 {
			w := windows[0]
			starts := lineStartOffsets(norm)
			startOff := starts[w]
			endOff := len(norm)
			if w+len(oldLines) < len(starts) {
				endOff = starts[w+len(oldLines)] - 1
			}
			start := w + 1
			return norm[:startOff] + newText + norm[endOff:], start, start + len(oldLines) - 1, editLevelLines, nil
		}
	}

	return "", 0, 0, "", errors.New("未找到匹配。文件可能已被修改，请重新 read_file 后重试；注意 old_text 须与文件内容逐字一致")
}

// normalizeNewlines 将 \r\n 统一归一为 \n，孤立 \r 保持原样。
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// findAll 返回 sub 在 s 中所有出现的起始字节偏移，sub 为空时返回 nil。
func findAll(s, sub string) []int {
	if sub == "" {
		return nil
	}
	var offsets []int
	off := 0
	for {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			return offsets
		}
		off += i
		offsets = append(offsets, off)
		off += len(sub)
	}
}

// lineAt 返回字节偏移 off 对应的 1-based 行号。
func lineAt(s string, off int) int {
	return 1 + strings.Count(s[:off], "\n")
}

// offsetLines 批量将字节偏移转换为 1-based 行号。
func offsetLines(s string, offsets []int) []int {
	lines := make([]int, len(offsets))
	for i, off := range offsets {
		lines[i] = lineAt(s, off)
	}
	return lines
}

// replaceAt 将 s 中 [start, start+length) 区间替换为 repl。
func replaceAt(s string, start, length int, repl string) string {
	return s[:start] + repl + s[start+length:]
}

// splitTrimLines 按 \n 切分并去除每行首尾空白。
func splitTrimLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return lines
}

// matchLines 在 fileLines 上以 len(oldLines) 为窗口大小滑动，
// 返回所有整窗相等命中的起始行下标（0-based）；入参行须已去首尾空白。
func matchLines(fileLines, oldLines []string) []int {
	k := len(oldLines)
	if k == 0 {
		return nil
	}
	var hits []int
	for i := 0; i+k <= len(fileLines); i++ {
		match := true
		for j := 0; j < k; j++ {
			if fileLines[i+j] != oldLines[j] {
				match = false
				break
			}
		}
		if match {
			hits = append(hits, i)
		}
	}
	return hits
}

// lineStartOffsets 返回 s 按 \n 切分后每行的起始字节偏移，
// 行数与 strings.Split(s, "\n") 一致。
func lineStartOffsets(s string) []int {
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// multiMatchError 生成多处命中的报错并附各行号，驱动模型扩大 old_text 上下文。
func multiMatchError(lineNos []int) error {
	parts := make([]string, len(lineNos))
	for i, n := range lineNos {
		parts[i] = strconv.Itoa(n)
	}
	return fmt.Errorf("old_text 在文件中匹配到 %d 处（第 %s 行），请扩大 old_text 范围加入上下文行使其唯一", len(lineNos), strings.Join(parts, "、"))
}

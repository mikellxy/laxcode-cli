package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/env"
)

func TestApplyEdit(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		oldText   string
		newText   string
		want      string // 期望替换后内容；为空表示期望报错
		start     int
		end       int
		level     string
		errSubstr string
	}{
		{
			name:    "L1 精确匹配唯一命中",
			content: "hello world",
			oldText: "world",
			newText: "golang",
			want:    "hello golang",
			start:   1, end: 1,
			level: editLevelExact,
		},
		{
			name:    "L1 纯 LF 文件其余字节不变",
			content: "a\nb\nc\n",
			oldText: "b",
			newText: "B",
			want:    "a\nB\nc\n",
			start:   2, end: 2,
			level: editLevelExact,
		},
		{
			name:    "L1 CRLF 文件命中且 new_text 按 LF 写入",
			content: "a\r\nb\r\n",
			oldText: "a\r\nb",
			newText: "X\nY",
			want:    "X\nY\r\n",
			start:   1, end: 2,
			level: editLevelExact,
		},
		{
			name:      "L1 多处命中报错并列出行号",
			content:   "a\nb\na\n",
			oldText:   "a\n",
			newText:   "X",
			errSubstr: "匹配到 2 处（第 1、3 行）",
		},
		{
			name:    "L2 CRLF 归一化后命中且全文件 LF 写回",
			content: "foo\r\nbar\r\nbaz\r\n",
			oldText: "foo\nbar",
			newText: "X",
			want:    "X\nbaz\n",
			start:   1, end: 2,
			level: editLevelNorm,
		},
		{
			name:      "L2 多处命中报错并列出行号",
			content:   "a\r\nb\r\na\r\n",
			oldText:   "a\n",
			newText:   "X",
			errSubstr: "匹配到 2 处（第 1、3 行）",
		},
		{
			name:    "L3 首尾空白被容忍且两侧空白保留",
			content: "v = old  \n",
			oldText: "  v = old\n\n",
			newText: "v = new",
			want:    "v = new  \n",
			start:   1, end: 1,
			level: editLevelTrim,
		},
		{
			name:      "L3 多处命中报错并列出行号",
			content:   "a\nb\na\n",
			oldText:   " a \n",
			newText:   "X",
			errSubstr: "匹配到 2 处（第 1、3 行）",
		},
		{
			name:    "L4 缩进差异行级命中且缩进以 new_text 为准",
			content: "if x {\n    return 1\n}\n",
			oldText: "if x {\n  return 1\n}",
			newText: "if x {\n\treturn 1\n}",
			want:    "if x {\n\treturn 1\n}\n",
			start:   1, end: 3,
			level: editLevelLines,
		},
		{
			name:      "L4 多处命中报错并列出行号",
			content:   "func A() {\n\treturn\n}\n\nfunc B() {\n\treturn\n}\n",
			oldText:   "\treturn  \n}",
			newText:   "X",
			errSubstr: "匹配到 2 处（第 2、6 行）",
		},
		{
			name:    "new_text 空串执行删除",
			content: "keep\ndrop\nkeep2\n",
			oldText: "drop\n",
			newText: "",
			want:    "keep\nkeep2\n",
			start:   2, end: 2,
			level: editLevelExact,
		},
		{
			name:      "全部未命中返回引导重新读取的错误",
			content:   "abc",
			oldText:   "xyz",
			newText:   "X",
			errSubstr: "未找到匹配。文件可能已被修改，请重新 read_file 后重试",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, start, end, level, err := applyEdit(tc.content, tc.oldText, tc.newText)
			if tc.errSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error containing %q, got %v", tc.errSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("content mismatch:\ngot  %q\nwant %q", got, tc.want)
			}
			if start != tc.start || end != tc.end {
				t.Fatalf("line range mismatch: got %d-%d, want %d-%d", start, end, tc.start, tc.end)
			}
			if level != tc.level {
				t.Fatalf("level mismatch: got %q, want %q", level, tc.level)
			}
		})
	}
}

func TestMatchLines(t *testing.T) {
	cases := []struct {
		name      string
		fileLines []string
		oldLines  []string
		want      []int
	}{
		{name: "唯一命中", fileLines: []string{"a", "b", "c"}, oldLines: []string{"b"}, want: []int{1}},
		{name: "多处命中", fileLines: []string{"a", "a"}, oldLines: []string{"a"}, want: []int{0, 1}},
		{name: "无命中", fileLines: []string{"a", "b"}, oldLines: []string{"c"}, want: nil},
		{name: "窗口贴文件首尾", fileLines: []string{"x", "y"}, oldLines: []string{"x", "y"}, want: []int{0}},
		{name: "窗口大于文件行数", fileLines: []string{"x"}, oldLines: []string{"x", "y"}, want: nil},
		{name: "空行窗口匹配空白行", fileLines: []string{"a", "", "b"}, oldLines: []string{""}, want: []int{1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchLines(tc.fileLines, tc.oldLines); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("matchLines mismatch: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEditFileTool(t *testing.T) {
	env.WorkDir = t.TempDir()
	ctx := context.Background()
	e := EditFileTool{}

	seed := func(rel, content string) string {
		t.Helper()
		target := filepath.Join(env.WorkDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("seed mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		return target
	}

	t.Run("落盘替换成功并返回行号与层级", func(t *testing.T) {
		target := seed("cmd/main/main.go", "package main\n\nfunc main() {\n}\n")
		args, _ := json.Marshal(map[string]string{
			"path":     "cmd/main/main.go",
			"old_text": "func main() {\n}",
			"new_text": "func main() {\n\tprintln(\"hi\")\n}",
		})
		out, err := e.Execute(ctx, args)
		if err != nil {
			t.Fatalf("edit: %v", err)
		}
		if want := "已在 cmd/main/main.go 第 3-4 行完成替换（精确匹配）"; out != want {
			t.Fatalf("output mismatch: got %q, want %q", out, want)
		}
		b, _ := os.ReadFile(target)
		if string(b) != "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n" {
			t.Fatalf("content mismatch: %q", b)
		}
	})

	t.Run("目标文件不存在报错并指路 write_file", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "nope.txt", "old_text": "a", "new_text": "b"})
		_, err := e.Execute(ctx, args)
		if err == nil || !strings.Contains(err.Error(), "write_file") {
			t.Fatalf("expected not-exist error pointing to write_file, got %v", err)
		}
	})

	t.Run("多处匹配报错且文件保持不变", func(t *testing.T) {
		target := seed("dup.txt", "a\nb\na\n")
		args, _ := json.Marshal(map[string]string{"path": "dup.txt", "old_text": "a\n", "new_text": "X"})
		_, err := e.Execute(ctx, args)
		if err == nil || !strings.Contains(err.Error(), "第 1、3 行") {
			t.Fatalf("expected multi-match error with line numbers, got %v", err)
		}
		b, _ := os.ReadFile(target)
		if string(b) != "a\nb\na\n" {
			t.Fatalf("file should be unchanged, got %q", b)
		}
	})

	t.Run("old_text 空白被拒绝", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "x.txt", "old_text": "  \n ", "new_text": "b"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected blank old_text error, got nil")
		}
	})

	t.Run("路径穿越被拒绝", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "../../evil.txt", "old_text": "a", "new_text": "b"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected path escape error, got nil")
		}
	})

	t.Run("绝对路径被拒绝", func(t *testing.T) {
		const absTarget = "/tmp/laxcode_edit_file_evil.txt"
		args, _ := json.Marshal(map[string]string{"path": absTarget, "old_text": "a", "new_text": "b"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected absolute path error, got nil")
		}
		if _, err := os.Stat(absTarget); !os.IsNotExist(err) {
			t.Fatalf("absolute path target should not exist, stat err: %v", err)
		}
	})

	t.Run("缺少必填参数报错", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"old_text": "a", "new_text": "b"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected missing path error, got nil")
		}
		args, _ = json.Marshal(map[string]string{"path": "x.txt", "new_text": "b"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected missing old_text error, got nil")
		}
		args, _ = json.Marshal(map[string]string{"path": "x.txt", "old_text": "a"})
		if _, err := e.Execute(ctx, args); err == nil {
			t.Fatal("expected missing new_text error, got nil")
		}
	})
}

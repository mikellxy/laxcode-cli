package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileTool(t *testing.T) {
	workDir := t.TempDir()
	ctx := context.Background()
	w := NewWriteFileTool(workDir)

	t.Run("创建新文件并自动创建父目录", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "a/b/c.txt", "content": "hello"})
		if _, err := w.Execute(ctx, args); err != nil {
			t.Fatalf("create: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(workDir, "a", "b", "c.txt"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(b) != "hello" {
			t.Fatalf("content mismatch: %q", b)
		}
	})

	t.Run("覆写已有文件", func(t *testing.T) {
		target := filepath.Join(workDir, "exist.txt")
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		args, _ := json.Marshal(map[string]string{"path": "exist.txt", "content": "new"})
		if _, err := w.Execute(ctx, args); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		b, _ := os.ReadFile(target)
		if string(b) != "new" {
			t.Fatalf("overwrite content mismatch: %q", b)
		}
	})

	t.Run("成功返回值格式", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "cmd/main/main.go", "content": "package main\n"})
		out, err := w.Execute(ctx, args)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		if want := "内容成功写入文件：cmd/main/main.go"; out != want {
			t.Fatalf("output mismatch: got %q, want %q", out, want)
		}
	})

	t.Run("路径穿越被拒绝", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "../../evil.txt", "content": "x"})
		if _, err := w.Execute(ctx, args); err == nil {
			t.Fatal("expected path escape error, got nil")
		}
	})

	t.Run("绝对路径被拒绝", func(t *testing.T) {
		const absTarget = "/tmp/laxcode_write_file_evil.txt"
		args, _ := json.Marshal(map[string]string{"path": absTarget, "content": "x"})
		if _, err := w.Execute(ctx, args); err == nil {
			t.Fatal("expected absolute path error, got nil")
		}
		if _, err := os.Stat(absTarget); !os.IsNotExist(err) {
			t.Fatalf("absolute path target should not exist, stat err: %v", err)
		}
	})

	t.Run("缺少必填参数报错", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"content": "x"})
		if _, err := w.Execute(ctx, args); err == nil {
			t.Fatal("expected missing path error, got nil")
		}
		args, _ = json.Marshal(map[string]string{"path": "x.txt"})
		if _, err := w.Execute(ctx, args); err == nil {
			t.Fatal("expected missing content error, got nil")
		}
	})
}

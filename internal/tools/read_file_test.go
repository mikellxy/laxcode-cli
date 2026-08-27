package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestReadFileTool(t *testing.T) {
	workDir := t.TempDir()
	ctx := context.Background()
	r := ReadFileTool{}

	seed := func(rel, content string) string {
		t.Helper()
		target := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("seed mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		return target
	}

	exec := func(args map[string]any) string {
		t.Helper()
		b, _ := json.Marshal(args)
		out, err := r.Execute(ctx, b)
		if err != nil {
			t.Fatalf("read_file: %v", err)
		}
		return out
	}

	t.Run("全量读取输出内容加已读完 footer", func(t *testing.T) {
		seed("small.txt", "aaa\nbbb\n")
		want := "aaa\nbbb\n\n(文件已读完，最后一行行号: 2)\n"
		if got := exec(map[string]any{"path": "small.txt"}); got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got, want)
		}
	})

	t.Run("末行无换行符也读到行号并标已读完", func(t *testing.T) {
		seed("noeol.txt", "aaa\nbbb")
		want := "aaa\nbbb\n\n(文件已读完，最后一行行号: 2)\n"
		if got := exec(map[string]any{"path": "noeol.txt"}); got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got, want)
		}
	})

	t.Run("起始行与字节偏移参数生效", func(t *testing.T) {
		seed("offset.txt", "11111111111\n22222222222\n33333333333\n44444444444")
		want := "222222222\n33333333333\n44444444444\n\n(文件已读完，最后一行行号: 4)\n"
		got := exec(map[string]any{"path": "offset.txt", "start_line_no": 2, "start_bytes": 3})
		if got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got, want)
		}
	})

	t.Run("超过行数上限时 footer 给出下一页参数", func(t *testing.T) {
		var sb strings.Builder
		for i := 1; i <= 2001; i++ {
			fmt.Fprintf(&sb, "line%04d\n", i)
		}
		seed("manylines.txt", sb.String())

		want := sb.String()[:2000*9] + "\n(文件未读完，最后一行行号: 2000，" +
			"续读请传 start_line_no=2001, start_bytes=1)\n"
		if got := exec(map[string]any{"path": "manylines.txt"}); got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got[:len(want)-1], want[:len(want)-1])
		}

		// 按 footer 指引续读最后一页
		want2 := "line2001\n\n(文件已读完，最后一行行号: 2001)\n"
		got2 := exec(map[string]any{"path": "manylines.txt", "start_line_no": 2001, "start_bytes": 1})
		if got2 != want2 {
			t.Fatalf("second page mismatch:\n got  %q\n want %q", got2, want2)
		}
	})

	t.Run("超长行被字节上限截断 footer 给出行内续读参数", func(t *testing.T) {
		seed("longline.txt", strings.Repeat("X", 60000)+"\n")

		want := strings.Repeat("X", readFileToolMaxReadBytes) +
			"\n(文件未读完，最后一行行号: 1，该行未读完整(已读 " +
			strconv.Itoa(readFileToolMaxReadBytes) + " 字节)，" +
			"续读请传 start_line_no=1, start_bytes=" + strconv.Itoa(readFileToolMaxReadBytes+1) + ")\n"
		if got := exec(map[string]any{"path": "longline.txt"}); got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got, want)
		}

		// 按行内偏移续读补齐该行
		rest := strings.Repeat("X", 60000-readFileToolMaxReadBytes) + "\n"
		want2 := rest + "\n(文件已读完，最后一行行号: 1)\n"
		got2 := exec(map[string]any{
			"path": "longline.txt", "start_line_no": 1, "start_bytes": readFileToolMaxReadBytes + 1,
		})
		if got2 != want2 {
			t.Fatalf("resume mismatch:\n got  %q\n want %q", got2, want2)
		}
	})

	t.Run("按 footer 指引续读拼接等于全量内容", func(t *testing.T) {
		// 2500 行、每行 30 字节：第 1707 行处触发 50KB 字节截断，覆盖行截断续读路径
		var sb strings.Builder
		for i := 1; i <= 2500; i++ {
			fmt.Fprintf(&sb, "%030d\n", i)
		}
		seed("paged.txt", sb.String())
		full := sb.String()

		// 模拟模型行为：照抄 footer 中"续读请传 start_line_no=X, start_bytes=Y"的参数
		resumeRe := regexp.MustCompile(`续读请传 start_line_no=(\d+), start_bytes=(\d+)`)

		var assembled strings.Builder
		args := map[string]any{"path": "paged.txt"}
		for page := 0; page < 10; page++ {
			out := exec(args)
			// 内容与 footer 以 "\n(" 分隔；截断页内容本身无行尾 \n（由续读补齐），
			// 完整页内容已带行尾 \n，两种情况均只拼接 idx 之前的内容
			idx := strings.LastIndex(out, "\n(")
			assembled.WriteString(out[:idx])
			if strings.Contains(out, "文件已读完") {
				if assembled.String() != full {
					t.Fatalf("assembled %d bytes, want %d bytes", assembled.Len(), len(full))
				}
				return
			}
			m := resumeRe.FindStringSubmatch(out)
			if m == nil {
				t.Fatalf("cannot parse resume params from footer: %q", out)
			}
			line, _ := strconv.Atoi(m[1])
			b, _ := strconv.Atoi(m[2])
			args = map[string]any{"path": "paged.txt", "start_line_no": line, "start_bytes": b}
		}
		t.Fatal("pagination did not finish in 10 pages")
	})

	t.Run("空文件输出文件为空", func(t *testing.T) {
		seed("empty.txt", "")
		if got := exec(map[string]any{"path": "empty.txt"}); got != "(文件为空)\n" {
			t.Fatalf("output mismatch: got %q", got)
		}
	})

	t.Run("起始行超出文件范围时提示", func(t *testing.T) {
		seed("twolines.txt", "a\nb\n")
		want := "(文件已读完，最后一行行号: 2（本次未读取到内容：起始行超出文件范围）)\n"
		got := exec(map[string]any{"path": "twolines.txt", "start_line_no": 100})
		if got != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", got, want)
		}
	})

	t.Run("文件不存在报错", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{"path": "no_such_file.txt"})
		if _, err := r.Execute(ctx, b); err == nil {
			t.Fatal("expected not-exist error, got nil")
		}
	})

	t.Run("相对路径基于工作目录而非进程目录", func(t *testing.T) {
		// 若实现误以进程 CWD 解析相对路径，将读到错误文件或报不存在
		seed("wd_only.txt", "from workdir")
		got := exec(map[string]any{"path": "wd_only.txt"})
		if !strings.HasPrefix(got, "from workdir\n") {
			t.Fatalf("should read file under env.WorkDir, got %q", got)
		}
	})

	t.Run("路径穿越被拒绝", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"path": "../../evil.txt"})
		if _, err := r.Execute(ctx, args); err == nil {
			t.Fatal("expected path escape error, got nil")
		}
	})

	t.Run("绝对路径被拒绝", func(t *testing.T) {
		const absTarget = "/tmp/laxcode_read_file_evil.txt"
		args, _ := json.Marshal(map[string]any{"path": absTarget})
		if _, err := r.Execute(ctx, args); err == nil {
			t.Fatal("expected absolute path error, got nil")
		}
	})

	t.Run("缺少必填参数报错", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{})
		if _, err := r.Execute(ctx, args); err == nil {
			t.Fatal("expected missing path error, got nil")
		}
		args, _ = json.Marshal(map[string]any{"path": "  "})
		if _, err := r.Execute(ctx, args); err == nil {
			t.Fatal("expected blank path error, got nil")
		}
	})
}

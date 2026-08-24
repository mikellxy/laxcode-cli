package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
)

// session 测试全部走纯函数路径（显式传临时目录构造），
// 不触碰包级全局 sessionDB，避免用例间共享状态。

func TestSession_AppendLoadRoundTrip(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	sess := newSession(workDir, "round-trip")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "读一下 main.go"})
	sess.Append(schema.Message{
		Role: schema.RoleAssistant,
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		},
	})
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "file content...", ToolCallID: "call_1"})

	loaded := loadSession(workDir, "round-trip")
	if !reflect.DeepEqual(sess.messages, loaded.messages) {
		t.Fatalf("round-trip 后历史不一致:\n got: %#v\nwant: %#v", loaded.messages, sess.messages)
	}
}

func TestSession_LoadSkipsBadAndBlankLines(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	histPath := filepath.Join(workDir, ".laxcode", ".session", "dirty", historyFile)
	if err := os.MkdirAll(filepath.Dir(histPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"role":"user","content":"第一问"}
坏行，不是 JSON

{"role":"assistant","content":"第一答"}
{"role":"user","content":"残缺` + "\n"
	if err := os.WriteFile(histPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadSession(workDir, "dirty")
	want := []schema.Message{
		{Role: schema.RoleUser, Content: "第一问"},
		{Role: schema.RoleAssistant, Content: "第一答"},
	}
	if !reflect.DeepEqual(loaded.messages, want) {
		t.Fatalf("坏行/空行未被正确跳过:\n got: %#v\nwant: %#v", loaded.messages, want)
	}
}

func TestSession_LoadMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	loaded := loadSession(workDir, "never-exists")
	if len(loaded.messages) != 0 {
		t.Fatalf("文件不存在的 session 应为空历史，got %d 条", len(loaded.messages))
	}
}

func TestSession_View(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "view")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "hi"})

	view := sess.View("SYS")
	if len(view) != 2 || view[0].Role != schema.RoleSystem || view[0].Content != "SYS" || view[1].Content != "hi" {
		t.Fatalf("View 应为 [system]+历史，got %#v", view)
	}

	// View 是只读组装：不影响内部状态，后续追加正常反映在新视图里
	sess.Append(schema.Message{Role: schema.RoleAssistant, Content: "hello"})
	view2 := sess.View("SYS")
	if len(view2) != 3 || view2[2].Content != "hello" {
		t.Fatalf("View 后追加应反映在后续视图中，got %#v", view2)
	}
	if len(view) != 2 {
		t.Fatalf("先前返回的视图不应被后续追加影响，got %#v", view)
	}
}

func TestSession_AppendCreatesDirLazily(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	sess := newSession(workDir, "lazy-dir")

	if _, err := os.Stat(filepath.Dir(sess.historyPath)); !os.IsNotExist(err) {
		t.Fatalf("未 Append 的空会话不应创建会话目录")
	}

	sess.Append(schema.Message{Role: schema.RoleUser, Content: "first"})
	if _, err := os.Stat(sess.historyPath); err != nil {
		t.Fatalf("Append 后 history.jsonl 应存在: %v", err)
	}
}

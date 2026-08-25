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

func TestSession_AppendAccumulatesTokenUsed(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "token-acc")

	// 零用量消息（用户输入）不影响统计，也不产生 meta.json 快照
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "第一问"})
	if sess.TokenUsed != (schema.TokenStatistics{}) || sess.WindowToken != (schema.TokenStatistics{}) {
		t.Fatalf("零用量消息不应改变统计: TokenUsed=%+v WindowToken=%+v", sess.TokenUsed, sess.WindowToken)
	}
	if _, err := os.Stat(sess.metaPath); !os.IsNotExist(err) {
		t.Fatalf("零用量消息不应触发 meta.json 写入")
	}

	sess.Append(schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "第一答",
		TokenUsed: schema.TokenStatistics{TokenInput: 70, TokenOutput: 5},
	})
	if sess.TokenUsed != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("累计消耗应累加 assistant 用量: %+v", sess.TokenUsed)
	}
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("窗口占用应刷新为该消息用量原值: %+v", sess.WindowToken)
	}

	// 其后的 user 消息不刷新窗口快照
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "第二问", ToolCallID: "call_1"})
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("零用量消息不应刷新窗口占用: %+v", sess.WindowToken)
	}

	sess.Append(schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "第二答",
		TokenUsed: schema.TokenStatistics{TokenInput: 100, TokenOutput: 8},
	})
	wantUsed := schema.TokenStatistics{TokenInput: 170, TokenOutput: 13}
	if sess.TokenUsed != wantUsed {
		t.Fatalf("累计消耗应为全部消息用量加和: got %+v want %+v", sess.TokenUsed, wantUsed)
	}
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 100, TokenOutput: 8}) {
		t.Fatalf("窗口占用应为最后一条非零用量消息原值: %+v", sess.WindowToken)
	}
}

func TestSession_MetaSnapshotWrittenOnUsageChange(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "meta-snap")

	sess.Append(schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "答",
		TokenUsed: schema.TokenStatistics{TokenInput: 70, TokenOutput: 5},
	})

	data, err := os.ReadFile(sess.metaPath)
	if err != nil {
		t.Fatalf("携带用量的消息追加后 meta.json 应存在: %v", err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("meta.json 应为合法 JSON: %v\n内容: %s", err, data)
	}
	if meta.Version != 1 {
		t.Fatalf("meta.json version 应为 1: %+v", meta)
	}
	if meta.TokenUsed != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("meta.json token_used 应与累计消耗一致: %+v", meta.TokenUsed)
	}
	if meta.WindowToken != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("meta.json window_token 应与窗口占用一致: %+v", meta.WindowToken)
	}

	// 后续零用量消息不重写快照：内容保持不变（user 输入与 tool 结果同）
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "追问"})
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "ls output...", ToolCallID: "call_1"})
	data2, err := os.ReadFile(sess.metaPath)
	if err != nil {
		t.Fatalf("meta.json 应仍存在: %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("零用量消息不应重写 meta.json:\n before: %s\n after:  %s", data, data2)
	}
}

func TestSession_LoadReplaysTokenStats(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	histPath := filepath.Join(workDir, ".laxcode", ".session", "replay", historyFile)
	if err := os.MkdirAll(filepath.Dir(histPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// 首行为无 token_used 键的旧行（记账启用前的历史），按零值处理
	content := `{"role":"user","content":"旧问题"}
{"role":"assistant","content":"旧回答"}
{"role":"assistant","content":"答一","token_used":{"token_input":10,"token_output":1}}
{"role":"user","content":"工具结果","tool_call_id":"call_1"}
{"role":"assistant","content":"答二","token_used":{"token_input":20,"token_output":2}}
{"role":"assistant","content":"答三","token_used":{"token_input":30,"token_output":3}}
`
	if err := os.WriteFile(histPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadSession(workDir, "replay")
	wantUsed := schema.TokenStatistics{TokenInput: 60, TokenOutput: 6}
	if loaded.TokenUsed != wantUsed {
		t.Fatalf("重放求和恢复累计消耗: got %+v want %+v", loaded.TokenUsed, wantUsed)
	}
	if loaded.WindowToken != (schema.TokenStatistics{TokenInput: 30, TokenOutput: 3}) {
		t.Fatalf("窗口占用应取最后一条非零用量消息原值: %+v", loaded.WindowToken)
	}
}

func TestSession_LoadIgnoresBrokenMeta(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".laxcode", ".session", "broken-meta")
	histPath := filepath.Join(dir, historyFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(histPath, []byte(`{"role":"assistant","content":"答","token_used":{"token_input":42,"token_output":7}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte("不是 JSON{{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadSession(workDir, "broken-meta")
	if loaded.TokenUsed != (schema.TokenStatistics{TokenInput: 42, TokenOutput: 7}) {
		t.Fatalf("meta.json 损坏时应以历史重放为准: %+v", loaded.TokenUsed)
	}
	if len(loaded.messages) != 1 {
		t.Fatalf("坏 meta.json 不应阻断历史加载，got %d 条", len(loaded.messages))
	}
}

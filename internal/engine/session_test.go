package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
)

// session 测试尽量走纯函数路径（显式传临时目录构造）；
// 仅 GetSession 缓存用例触碰全局 sessionDB，以独立 session id 隔离。

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
	if !reflect.DeepEqual(sess.Messages, loaded.Messages) {
		t.Fatalf("round-trip 后历史不一致:\n got: %#v\nwant: %#v", loaded.Messages, sess.Messages)
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
	if !reflect.DeepEqual(loaded.Messages, want) {
		t.Fatalf("坏行/空行未被正确跳过:\n got: %#v\nwant: %#v", loaded.Messages, want)
	}
}

func TestSession_LoadMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	loaded := loadSession(workDir, "never-exists")
	if len(loaded.Messages) != 0 {
		t.Fatalf("文件不存在的 session 应为空历史，got %d 条", len(loaded.Messages))
	}
}

func TestGetSession_CacheHitSkipsReload(t *testing.T) {
	// 不 Parallel：触碰全局 sessionDB。其余用例不经过 GetSession，无竞争
	workDir := t.TempDir()
	sessID := "cache-hit"

	// 首次未命中：磁盘装配（注入 system prompt）并写入缓存
	first := GetSession(workDir, sessID, false)
	if len(first.Messages) != 1 || first.Messages[0].Role != schema.RoleSystem {
		t.Fatalf("首次装配应注入 system prompt: %#v", first.Messages)
	}
	first.Append(schema.Message{Role: schema.RoleUser, Content: "内存里的问题"})

	// 绕过内存直接向 history.jsonl 追加一行，使磁盘比内存新
	line, err := json.Marshal(schema.Message{Role: schema.RoleUser, Content: "磁盘上的新问题"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(first.historyPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// 第二次命中缓存：返回同一对象，不感知磁盘新增行
	second := GetSession(workDir, sessID, false)
	if second != first {
		t.Fatalf("缓存命中应返回同一 session 对象")
	}
	if len(second.Messages) != 2 {
		t.Fatalf("缓存命中不应重新加载磁盘: got %d 条消息, want 2", len(second.Messages))
	}
}

func TestSession_View(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "view")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "hi"})

	// View 就地注入：system prompt 进入会话历史头部，供 Generate 直接使用
	sess.View("SYS")
	msgs := sess.Messages
	if len(msgs) != 2 || msgs[0].Role != schema.RoleSystem || msgs[0].Content != "SYS" || msgs[1].Content != "hi" {
		t.Fatalf("View 应为 [system]+历史，got %#v", msgs)
	}

	// 注入后追加的消息正常出现在历史尾部
	sess.Append(schema.Message{Role: schema.RoleAssistant, Content: "hello"})
	if len(sess.Messages) != 3 || sess.Messages[2].Content != "hello" {
		t.Fatalf("View 后追加应正常进入历史，got %#v", sess.Messages)
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

func TestSession_AppendDoesNotAccountTokens(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "append-no-account")

	// Append 不再承担记账：即使消息携带用量，统计也不变、不落 meta.json
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "第一问"})
	sess.Append(schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "第一答",
		TokenUsed: schema.TokenStatistics{TokenInput: 70, TokenOutput: 5},
	})
	if sess.TokenUsed != (schema.TokenStatistics{}) || sess.WindowToken != (schema.TokenStatistics{}) {
		t.Fatalf("Append 不应改变统计: TokenUsed=%+v WindowToken=%+v", sess.TokenUsed, sess.WindowToken)
	}
	if len(sess.Rounds) != 0 {
		t.Fatalf("Append 不应产生观测记录: %+v", sess.Rounds)
	}
	if _, err := os.Stat(sess.metaPath); !os.IsNotExist(err) {
		t.Fatalf("Append 不应触发 meta.json 写入")
	}
}

func TestSession_RecordGenerateAccountsAndRounds(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "record-generate")

	sess.RecordGenerate(RoundStat{TimeUsed: 120, TokenInput: 70, TokenOutput: 5})
	if sess.TokenUsed != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("累计消耗应累加本轮用量: %+v", sess.TokenUsed)
	}
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("窗口占用应刷新为本轮用量原值: %+v", sess.WindowToken)
	}

	sess.RecordGenerate(RoundStat{TimeUsed: 230, TokenInput: 100, TokenOutput: 8})
	wantUsed := schema.TokenStatistics{TokenInput: 170, TokenOutput: 13}
	if sess.TokenUsed != wantUsed {
		t.Fatalf("累计消耗应为各轮用量加和: got %+v want %+v", sess.TokenUsed, wantUsed)
	}
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 100, TokenOutput: 8}) {
		t.Fatalf("窗口占用应为最后一轮用量原值: %+v", sess.WindowToken)
	}

	wantRounds := []RoundStat{
		{TimeUsed: 120, TokenInput: 70, TokenOutput: 5},
		{TimeUsed: 230, TokenInput: 100, TokenOutput: 8},
	}
	if !reflect.DeepEqual(sess.Rounds, wantRounds) {
		t.Fatalf("Rounds 应逐轮追加观测记录: got %+v want %+v", sess.Rounds, wantRounds)
	}
}

func TestSession_MetaSnapshotWrittenOnRecordGenerate(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "meta-snap")

	sess.RecordGenerate(RoundStat{TimeUsed: 120, TokenInput: 70, TokenOutput: 5})

	data, err := os.ReadFile(sess.metaPath)
	if err != nil {
		t.Fatalf("RecordGenerate 后 meta.json 应存在: %v", err)
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
	wantRounds := []RoundStat{{TimeUsed: 120, TokenInput: 70, TokenOutput: 5}}
	if !reflect.DeepEqual(meta.Rounds, wantRounds) {
		t.Fatalf("meta.json rounds 应为最新每轮观测列表: got %+v want %+v", meta.Rounds, wantRounds)
	}
	// 列表元素的 JSON 键须为 time_used/token_input/token_output
	if !strings.Contains(string(data), `"time_used"`) {
		t.Fatalf("meta.json rounds 元素应含 time_used 键: %s", data)
	}

	// 新一轮上报后快照整体覆写：rounds 列表随之增长，Append 不改快照
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "追问"})
	sess.RecordGenerate(RoundStat{TimeUsed: 230, TokenInput: 100, TokenOutput: 8})
	data2, err := os.ReadFile(sess.metaPath)
	if err != nil {
		t.Fatalf("meta.json 应仍存在: %v", err)
	}
	var meta2 SessionMeta
	if err := json.Unmarshal(data2, &meta2); err != nil {
		t.Fatalf("meta.json 应为合法 JSON: %v\n内容: %s", err, data2)
	}
	if len(meta2.Rounds) != 2 || meta2.Rounds[1] != (RoundStat{TimeUsed: 230, TokenInput: 100, TokenOutput: 8}) {
		t.Fatalf("覆写后的 rounds 应含两轮观测: %+v", meta2.Rounds)
	}
	if meta2.TokenUsed != (schema.TokenStatistics{TokenInput: 170, TokenOutput: 13}) {
		t.Fatalf("覆写后的 token_used 应为两轮加和: %+v", meta2.TokenUsed)
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
	if len(loaded.Messages) != 1 {
		t.Fatalf("坏 meta.json 不应阻断历史加载，got %d 条", len(loaded.Messages))
	}
}

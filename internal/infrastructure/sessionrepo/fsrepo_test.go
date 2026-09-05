package sessionrepo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestAppendAndGetMessagesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := NewFsSessionRepo(dir)
	ctx := context.Background()
	sid := "sess-1"

	msgs := []*sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: "sys"},
		{Role: sharedkernel.RoleUser, Content: "hi"},
		{Role: sharedkernel.RoleAssistant, Content: "hello",
			ReasoningID: "rsn-1", ReasoningContent: "think",
			ToolCalls: []sharedkernel.ToolCall{
				{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
			},
			TokenUsed: sharedkernel.TokenStatistics{TokenInput: 50, TokenOutput: 8},
		},
	}
	for _, m := range msgs {
		if err := repo.AppendMessage(ctx, sid, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	got, err := repo.GetMessages(ctx, sid)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("应读回 %d 条消息，实际 %d", len(msgs), len(got))
	}
	for i, want := range msgs {
		g := got[i]
		if g.Role != want.Role || g.Content != want.Content {
			t.Errorf("第 %d 条 role/content 不符：%+v", i, g)
		}
		if g.TokenUsed != want.TokenUsed {
			t.Errorf("第 %d 条 token_used 不符：%+v", i, g.TokenUsed)
		}
		if want.ReasoningID != "" && g.ReasoningID != want.ReasoningID {
			t.Errorf("第 %d 条 reasoning_id 不符：%q", i, g.ReasoningID)
		}
		if want.ToolCalls != nil {
			if len(g.ToolCalls) != 1 || g.ToolCalls[0].Name != "bash" ||
				string(g.ToolCalls[0].Arguments) != `{"command":"ls"}` {
				t.Errorf("第 %d 条 tool_calls 不符：%+v", i, g.ToolCalls)
			}
		}
	}
}

func TestGetMessagesMissingSessionReturnsNil(t *testing.T) {
	repo := NewFsSessionRepo(t.TempDir())
	got, err := repo.GetMessages(context.Background(), "never-exists")
	if err != nil {
		t.Fatalf("不存在的会话不应报错：%v", err)
	}
	if got != nil {
		t.Errorf("不存在的会话应返回 nil，实际 %+v", got)
	}
}

func TestGetMetaMissingSessionReturnsZero(t *testing.T) {
	repo := NewFsSessionRepo(t.TempDir())
	meta, err := repo.GetMeta(context.Background(), "never-exists")
	if err != nil {
		t.Fatalf("不存在的会话不应报错：%v", err)
	}
	if meta == nil || meta.TokenUsed != (sharedkernel.TokenStatistics{}) || meta.WindowToken != (sharedkernel.TokenStatistics{}) {
		t.Errorf("缺省 meta 应为零值，实际 %+v", meta)
	}
}

func TestUpdateAndGetMeta(t *testing.T) {
	dir := t.TempDir()
	repo := NewFsSessionRepo(dir)
	ctx := context.Background()
	sid := "sess-meta"

	m1 := &sharedkernel.SessionMeta{
		TokenUsed:   sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20},
		WindowToken: sharedkernel.TokenStatistics{TokenInput: 80, TokenOutput: 10},
	}
	if err := repo.UpdateMeta(ctx, sid, m1); err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	got, err := repo.GetMeta(ctx, sid)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if *got != *m1 {
		t.Errorf("meta 回读不符：got %+v want %+v", got, m1)
	}

	// 覆写
	m2 := &sharedkernel.SessionMeta{TokenUsed: sharedkernel.TokenStatistics{TokenInput: 200, TokenOutput: 40}}
	if err := repo.UpdateMeta(ctx, sid, m2); err != nil {
		t.Fatalf("UpdateMeta overwrite: %v", err)
	}
	got2, _ := repo.GetMeta(ctx, sid)
	if got2.TokenUsed.TokenInput != 200 {
		t.Errorf("meta 覆写失败：%+v", got2)
	}
}

func TestAppendMessageCreatesSessionDir(t *testing.T) {
	dir := t.TempDir()
	repo := NewFsSessionRepo(dir)
	if err := repo.AppendMessage(context.Background(), "sess-dir", &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "x"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	path := filepath.Join(dir, "sess-dir", historyFile)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("应创建会话目录下的 history.jsonl：%v", err)
	}
}

func TestGetMessagesSkipsCorruptAndBlankLines(t *testing.T) {
	dir := t.TempDir()
	sid := "sess-bad"
	sessDir := filepath.Join(dir, sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good, _ := json.Marshal(&sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "ok"})
	content := string(good) + "\n" + "\n" + "not-json\n" + string(good) + "\n"
	if err := os.WriteFile(filepath.Join(sessDir, historyFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	repo := NewFsSessionRepo(dir)
	got, err := repo.GetMessages(context.Background(), sid)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	// 空白行与损坏行应被静默跳过
	if len(got) != 2 {
		t.Errorf("应仅解析出 2 条合法消息，实际 %d：%+v", len(got), got)
	}
	for _, m := range got {
		if m.Content != "ok" {
			t.Errorf("解析内容不符：%+v", m)
		}
	}
}

func TestGetMessagesBigLine(t *testing.T) {
	// scanner 缓冲区放大后仍能读取超长单行消息
	dir := t.TempDir()
	sid := "sess-big"
	sessDir := filepath.Join(dir, sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := make([]byte, 1024*1024)
	for i := range big {
		big[i] = 'x'
	}
	line, _ := json.Marshal(&sharedkernel.Message{Role: sharedkernel.RoleUser, Content: string(big)})
	if err := os.WriteFile(filepath.Join(sessDir, historyFile), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	repo := NewFsSessionRepo(dir)
	got, err := repo.GetMessages(context.Background(), sid)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 1 || len(got[0].Content) != len(big) {
		t.Errorf("超长消息应完整读回，实际 %d 条/%d 字节", len(got), len(got[0].Content))
	}
}

// 编译期：FsSessionRepo 必须满足领域仓库所需的持久化方法集合（行为层面
// 与 domain/session 的 SessionRepository 由使用侧保证）。
func TestFsSessionRepoUsesAppendMode(t *testing.T) {
	dir := t.TempDir()
	repo := NewFsSessionRepo(dir)
	ctx := context.Background()
	sid := "sess-append"
	for i := 0; i < 3; i++ {
		if err := repo.AppendMessage(ctx, sid, &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "m"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := repo.GetMessages(ctx, sid)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("追加模式应保留全部 3 条，实际 %d", len(got))
	}
}

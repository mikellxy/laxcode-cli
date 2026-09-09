package compactor

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func assistantToolTurn(ids ...string) sharedkernel.Message {
	calls := make([]sharedkernel.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, sharedkernel.ToolCall{
			ID: id, Name: "tool_" + id, Arguments: json.RawMessage(`{"value":"x"}`),
		})
	}
	return sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: calls}
}

func TestCompressZeroSavingsIsNoopAndDoesNotAliasSlice(t *testing.T) {
	msgs := []sharedkernel.Message{{Role: sharedkernel.RoleUser, Content: "question"}}
	out, saved, err := SimpleCompactor.Compress(msgs, 0)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved != 0 || out[0].Content != msgs[0].Content {
		t.Fatalf("零目标不应压缩：saved=%d out=%+v", saved, out)
	}
	out[0].Content = "changed"
	if msgs[0].Content != "question" {
		t.Fatal("返回切片不应与原始历史别名")
	}
}

func TestCompressUsesRoleToolAndKeepsLatestParallelSpan(t *testing.T) {
	oldOutput := strings.Repeat("旧结果", 500)
	latestA := strings.Repeat("A", 800)
	latestB := strings.Repeat("B", 800)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		assistantToolTurn("old"),
		{Role: sharedkernel.RoleTool, ToolCallID: "old", Content: oldOutput},
		{Role: sharedkernel.RoleAssistant, Content: "old done"},
		assistantToolTurn("new-a", "new-b"),
		{Role: sharedkernel.RoleTool, ToolCallID: "new-a", Content: latestA},
		{Role: sharedkernel.RoleTool, ToolCallID: "new-b", Content: latestB},
	}

	out, saved, err := SimpleCompactor.Compress(msgs, 1)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved <= 0 || !strings.Contains(out[2].Content, "早期工具输出已清理") {
		t.Fatalf("真实 RoleTool 的旧 span 应被清理：saved=%d content=%q", saved, out[2].Content)
	}
	if out[5].Content != latestA || out[6].Content != latestB {
		t.Fatal("同一最新并行 tool-call span 的所有结果应一起完整保留")
	}
	if len(out[4].ToolCalls) != 2 || out[5].ToolCallID != "new-a" || out[6].ToolCallID != "new-b" {
		t.Fatalf("function call/result 配对被破坏：%+v", out[4:])
	}
}

func TestCompressLatestParallelSpanTruncatesEveryLargeResultUTF8Safely(t *testing.T) {
	largeA := strings.Repeat("你🙂", 900)
	largeB := strings.Repeat("界🚀", 900)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		assistantToolTurn("a", "b"),
		{Role: sharedkernel.RoleTool, ToolCallID: "a", Content: largeA},
		{Role: sharedkernel.RoleTool, ToolCallID: "b", Content: largeB},
	}

	out, saved, err := SimpleCompactor.Compress(msgs, 1_000_000)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved <= 0 {
		t.Fatal("large latest tool results should be truncated")
	}
	for _, idx := range []int{2, 3} {
		if !utf8.ValidString(out[idx].Content) {
			t.Fatalf("result %d is not valid UTF-8", idx)
		}
		if !strings.Contains(out[idx].Content, "...") {
			t.Fatalf("result %d was not truncated", idx)
		}
	}
	if len(out[1].ToolCalls) != 2 || out[2].ToolCallID != "a" || out[3].ToolCallID != "b" {
		t.Fatal("truncation must preserve the complete tool-call span")
	}
}

func TestCompressClearsOldReasoningWithinSingleUserTask(t *testing.T) {
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "one long task"},
		{Role: sharedkernel.RoleAssistant, ReasoningContent: strings.Repeat("old reasoning", 200)},
		{Role: sharedkernel.RoleAssistant, ReasoningContent: "latest reasoning"},
	}
	out, saved, err := SimpleCompactor.Compress(msgs, 1)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved <= 0 || out[1].ReasoningContent != "" {
		t.Fatal("old reasoning must be cleared within a single long user task")
	}
	if out[2].ReasoningContent != "latest reasoning" {
		t.Fatal("the latest assistant reasoning must be retained")
	}
}

func TestCompressKeepsSystemAndUserMessages(t *testing.T) {
	system := strings.Repeat("system", 1000)
	user := strings.Repeat("user", 1000)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: system},
		{Role: sharedkernel.RoleUser, Content: user},
	}
	out, saved, err := SimpleCompactor.Compress(msgs, 1_000_000)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved != 0 || out[0].Content != system || out[1].Content != user {
		t.Fatal("system and user messages must not be truncated")
	}
}

func TestCompressRejectsNegativeSavings(t *testing.T) {
	if _, _, err := SimpleCompactor.Compress(nil, -1); err == nil {
		t.Fatal("negative savings target must fail")
	}
}

func TestSatisfiesSessionCompactorPort(t *testing.T) {
	var _ session.Compactor = SimpleCompactor
}

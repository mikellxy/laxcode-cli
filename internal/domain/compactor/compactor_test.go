package compactor

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
	midOutput := strings.Repeat("中", 800)
	latestA := strings.Repeat("A", 800)
	latestB := strings.Repeat("B", 800)
	// 4 个工具调用轮次 > reActToolCallTurnKept(3)：最旧 span 应被清理，
	// 最近 3 个 span（含最新并行 span）的结果完整保留。
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		assistantToolTurn("old"), // span0：最旧，窗口之外，应被清理
		{Role: sharedkernel.RoleTool, ToolCallID: "old", Content: oldOutput, Artifact: &sharedkernel.ArtifactRef{ID: strings.Repeat("a", 64), ByteSize: len(oldOutput)}},
		{Role: sharedkernel.RoleAssistant, Content: "old done"},
		assistantToolTurn("mid"), // span1：最近 3 之内
		{Role: sharedkernel.RoleTool, ToolCallID: "mid", Content: midOutput},
		assistantToolTurn("mid2"), // span2：最近 3 之内
		{Role: sharedkernel.RoleTool, ToolCallID: "mid2", Content: midOutput},
		assistantToolTurn("new-a", "new-b"), // span3：最新并行 span
		{Role: sharedkernel.RoleTool, ToolCallID: "new-a", Content: latestA},
		{Role: sharedkernel.RoleTool, ToolCallID: "new-b", Content: latestB},
	}

	out, saved, err := SimpleCompactor.Compress(msgs, 1)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved <= 0 || !strings.Contains(out[2].Content, "read_artifact") {
		t.Fatalf("窗口之外的旧 span 应被清理：saved=%d content=%q", saved, out[2].Content)
	}
	if out[9].Content != latestA || out[10].Content != latestB {
		t.Fatal("同一最新并行 tool-call span 的所有结果应一起完整保留")
	}
	if len(out[8].ToolCalls) != 2 || out[9].ToolCallID != "new-a" || out[10].ToolCallID != "new-b" {
		t.Fatalf("function call/result 配对被破坏：%+v", out[8:])
	}
	// 最近窗口之内、但不是最新的 span 结果不应被“已清理”占位符替换
	if strings.Contains(out[5].Content, "早期工具输出已清理") {
		t.Fatal("最近 reActToolCallTurnKept 个 span 的结果不应被清理")
	}
}

func TestCompressNeverTruncatesProtectedParallelResults(t *testing.T) {
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
	if saved != 0 || !reflect.DeepEqual(out, msgs) {
		t.Fatal("protected messages must remain byte-for-byte unchanged")
	}
	if len(out[1].ToolCalls) != 2 || out[2].ToolCallID != "a" || out[3].ToolCallID != "b" {
		t.Fatal("truncation must preserve the complete tool-call span")
	}
}

func TestCompressClearsOldReasoningWithinSingleUserTask(t *testing.T) {
	oldReasoning := strings.Repeat("old reasoning", 200)
	turn := func(id, reasoning string) sharedkernel.Message {
		m := assistantToolTurn(id)
		m.ReasoningContent = reasoning
		return m
	}
	// 4 个工具调用轮次 > reActToolCallTurnKept(3)：最旧轮次的 reasoning 应被回收，
	// 最近 3 轮的 reasoning 完整保留。
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "one long task"},
		turn("t1", oldReasoning), // 最旧，窗口之外，应被回收
		{Role: sharedkernel.RoleTool, ToolCallID: "t1", Content: "r1"},
		turn("t2", "keep reasoning 2"),
		{Role: sharedkernel.RoleTool, ToolCallID: "t2", Content: "r2"},
		turn("t3", "keep reasoning 3"),
		{Role: sharedkernel.RoleTool, ToolCallID: "t3", Content: "r3"},
		turn("t4", "keep reasoning 4"),
		{Role: sharedkernel.RoleTool, ToolCallID: "t4", Content: "r4"},
	}
	out, saved, err := SimpleCompactor.Compress(msgs, 1)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if saved <= 0 || out[1].ReasoningContent != "" {
		t.Fatalf("窗口之外的旧 reasoning 必须被回收：saved=%d reasoning=%q", saved, out[1].ReasoningContent)
	}
	for _, idx := range []int{3, 5, 7} {
		if out[idx].ReasoningContent == "" {
			t.Fatalf("最近 reActToolCallTurnKept 轮的 reasoning 应保留：idx=%d", idx)
		}
	}
}

func TestCompressKeepsEverythingAndDoesNotPanicWithoutToolSpans(t *testing.T) {
	// 无任何工具调用、但消息数 >= reActToolCallTurnKept：旧实现会因
	// len(out) 误用于索引 spans 而 panic；修复后应正常返回，且因边界为 -1
	// 保留全部 assistant 正文（无“旧工具轮次”可锚定裁剪）。
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: strings.Repeat("a", 2000)},
		{Role: sharedkernel.RoleAssistant, Content: strings.Repeat("b", 2000)},
		{Role: sharedkernel.RoleAssistant, Content: strings.Repeat("c", 2000)},
		{Role: sharedkernel.RoleUser, Content: strings.Repeat("d", 2000)},
	}
	out, _, err := SimpleCompactor.Compress(msgs, 1)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("消息数量不应变化：%d != %d", len(out), len(msgs))
	}
	if out[1].Content != msgs[1].Content || out[2].Content != msgs[2].Content {
		t.Fatal("无工具调用轮次时不应裁剪 assistant 正文")
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

package compactor

import (
	"reflect"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestMergeSummaryReplacesUnprotectedHistoryAndCombinesOrigins(t *testing.T) {
	msgs := []sharedkernel.Message{
		{Seq: 1, OriginalSeq: []uint64{1}, Role: sharedkernel.RoleSystem, Content: "system"},
		{Seq: 2, OriginalSeq: []uint64{2, 3}, Role: sharedkernel.RoleUser, Content: "old summary"},
		{Seq: 4, OriginalSeq: []uint64{4}, Role: sharedkernel.RoleAssistant, Content: "old answer"},
		{Seq: 5, OriginalSeq: []uint64{5}, Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: "keep"}}},
		{Seq: 6, OriginalSeq: []uint64{6}, Role: sharedkernel.RoleTool, ToolCallID: "keep", Content: "result"},
	}

	out, err := MergeSummary(msgs, 3, `{"objective":"continue"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 || out[0].Content != "system" || out[2].Seq != 5 || out[3].Seq != 6 {
		t.Fatalf("unexpected merged messages: %+v", out)
	}
	summary := out[1]
	if summary.Role != sharedkernel.RoleUser || summary.Seq != 2 ||
		!reflect.DeepEqual(summary.OriginalSeq, []uint64{2, 3, 4}) {
		t.Fatalf("unexpected summary identity: %+v", summary)
	}
	out[1].OriginalSeq[0] = 99
	if msgs[1].OriginalSeq[0] != 2 {
		t.Fatal("merged summary aliases source original sequences")
	}
}

func TestMergeSummaryRejectsMissingSummarizableRange(t *testing.T) {
	msgs := []sharedkernel.Message{
		{Seq: 1, OriginalSeq: []uint64{1}, Role: sharedkernel.RoleSystem},
		{Seq: 2, OriginalSeq: []uint64{2}, Role: sharedkernel.RoleUser},
	}
	if _, err := MergeSummary(msgs, 1, "summary"); err == nil {
		t.Fatal("accepted empty summary range")
	}
}

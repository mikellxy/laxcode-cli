package compactor

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestProtectionIncludesAllMessagesBetweenRecentGroups(t *testing.T) {
	large := strings.Repeat("中文🙂", 1500)
	msgs := []sharedkernel.Message{
		assistantToolTurn("old"),
		{Role: sharedkernel.RoleTool, ToolCallID: "old", Content: large, Artifact: &sharedkernel.ArtifactRef{ID: strings.Repeat("a", 64), ByteSize: len(large)}},
		{Role: sharedkernel.RoleAssistant, Content: large, ReasoningContent: large},
	}
	start := len(msgs)
	for _, id := range []string{"g2", "g3", "g4"} {
		msgs = append(msgs, assistantToolTurn(id), sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: large})
		for i := 0; i < 3; i++ {
			msgs = append(msgs, sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: large, ReasoningContent: large})
		}
		msgs = append(msgs, sharedkernel.Message{Role: sharedkernel.RoleUser, Content: large})
	}
	if ProtectedStart(msgs) != start {
		t.Fatal("incorrect protection boundary")
	}
	out, _, err := SimpleCompactor.Compress(msgs, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out[start:], msgs[start:]) {
		t.Fatal("modified protected interval with more than three assistant messages")
	}
	if out[2].ReasoningContent != "" || out[2].Content == large || !utf8.ValidString(out[2].Content) {
		t.Fatal("old ordinary assistant was not safely compacted")
	}
	if !strings.Contains(out[1].Content, "read_artifact") {
		t.Fatal("missing recoverable artifact reference")
	}
}

func TestUnfinishedAndCrossBoundaryGroupsExtendProtection(t *testing.T) {
	for _, lateResult := range []bool{false, true} {
		msgs := []sharedkernel.Message{assistantToolTurn("old")}
		for _, id := range []string{"a", "b", "c"} {
			msgs = append(msgs, assistantToolTurn(id), sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: "ok"})
		}
		if lateResult {
			msgs = append(msgs, sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: "old", Content: "late"})
		}
		if ProtectedStart(msgs) != 0 {
			t.Fatal("left unfinished/crossing group outside protection")
		}
	}
}

func TestUnarchivedToolResultsAreNeverDiscarded(t *testing.T) {
	var msgs []sharedkernel.Message
	for _, id := range []string{"old", "a", "b", "c"} {
		msgs = append(msgs, assistantToolTurn(id), sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: strings.Repeat("result", 500)})
	}
	indices := ArtifactCandidates(msgs)
	if !reflect.DeepEqual(indices, []int{1}) {
		t.Fatalf("wrong archive candidates: %v", indices)
	}
	out, saved, err := SimpleCompactor.Compress(msgs, 100000)
	if err != nil || saved != 0 || !reflect.DeepEqual(out, msgs) {
		t.Fatal("discarded tool output without successful archive")
	}
}

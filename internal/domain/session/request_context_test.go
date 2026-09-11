package session

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestSequencesAndOriginsSurviveSnapshotAndResume(t *testing.T) {
	s := NewSession("ids")
	s.UpsertSysMessage("system")
	messages := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: "a", Arguments: json.RawMessage(`{}`)}, {ID: "b"}}},
		{Role: sharedkernel.RoleTool, ToolCallID: "b", Content: "B"},
		{Role: sharedkernel.RoleTool, ToolCallID: "a", Content: "A"},
		{Role: sharedkernel.RoleAssistant, Content: "done"},
	}
	for i := range messages {
		if err := s.AppendMessage(&messages[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, msg := range s.Messages {
		want := uint64(i + 1)
		if msg.Seq != want || !reflect.DeepEqual(msg.OriginalSeq, []uint64{want}) {
			t.Fatalf("message %d identity=(%d,%v), want (%d,[%d])", i, msg.Seq, msg.OriginalSeq, want, want)
		}
	}
	detached := s.Snapshot()
	detached.Messages[1].OriginalSeq[0] = 99
	if s.Messages[1].OriginalSeq[0] != 2 {
		t.Fatal("snapshot aliased original sequence array")
	}
	snapshot := s.Snapshot()
	resumed := NewSession("ids")
	if err := resumed.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Messages, resumed.Messages) {
		t.Fatal("restore changed messages")
	}
	newMsg := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "continue"}
	if err := resumed.AppendMessage(&newMsg); err != nil {
		t.Fatal(err)
	}
	if newMsg.Seq != 7 || !reflect.DeepEqual(newMsg.OriginalSeq, []uint64{7}) {
		t.Fatalf("identity reused after restart: %+v", newMsg)
	}
	resumed.Messages[2].ToolCalls[0].Arguments[0] = 'x'
	if string(s.Messages[2].ToolCalls[0].Arguments) != "{}" {
		t.Fatal("snapshot aliased tool arguments")
	}
}

func TestInvalidSnapshotDoesNotReplaceWorkingContext(t *testing.T) {
	s := NewSession("bad")
	s.UpsertSysMessage("keep")
	before := s.Snapshot()
	for _, mutate := range []func(*RequestContext){
		func(r *RequestContext) { r.MemoryGeneration = 0 },
		func(r *RequestContext) { r.LastSeq++ },
		func(r *RequestContext) { r.Messages[0].OriginalSeq = []uint64{99} },
		func(r *RequestContext) { r.Messages[0].OriginalSeq = []uint64{1, 1} },
		func(r *RequestContext) { r.Messages[0].Role = sharedkernel.RoleUser },
	} {
		bad := before.Clone()
		mutate(&bad)
		if err := s.Restore(bad); err == nil {
			t.Fatal("accepted invalid snapshot")
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("failed restore changed session")
		}
	}
}

func TestAdvanceMemoryGeneration(t *testing.T) {
	s := NewSession("generation")
	if s.MemoryGeneration != 1 {
		t.Fatalf("initial generation=%d", s.MemoryGeneration)
	}
	if err := s.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	if s.MemoryGeneration != 2 {
		t.Fatalf("advanced generation=%d", s.MemoryGeneration)
	}
	s.MemoryGeneration = ^uint64(0)
	if err := s.AdvanceMemoryGeneration(); err == nil {
		t.Fatal("generation overflow was accepted")
	}
}

func TestAppendCandidateIsolatesSliceAndIncomingMessage(t *testing.T) {
	s := NewSession("append")
	s.UpsertSysMessage("system")
	old := sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		ToolCalls: []sharedkernel.ToolCall{{ID: "old", Arguments: json.RawMessage(`{}`)}},
		Artifact:  &sharedkernel.ArtifactRef{ID: "old-artifact"},
	}
	if err := s.AppendMessage(&old); err != nil {
		t.Fatal(err)
	}
	backing := make([]sharedkernel.Message, 4)
	copy(backing, s.Messages)
	backing[2].Content = "unused capacity sentinel"
	s.Messages = backing[:2]
	before := s.Snapshot()
	incoming := sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		ToolCalls: []sharedkernel.ToolCall{{ID: "new", Arguments: json.RawMessage(`{"n":1}`)}},
		Artifact:  &sharedkernel.ArtifactRef{ID: "new-artifact"},
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20},
	}
	candidate, err := s.WithAppendedMessage(&incoming)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) || backing[2].Content != "unused capacity sentinel" {
		t.Fatal("append candidate changed the original session or backing array")
	}
	if candidate.LastSeq != s.LastSeq+1 || candidate.TokenUsed.TokenInput != 100 {
		t.Fatal("candidate state was not advanced")
	}
	incoming.ToolCalls[0].Arguments[0] = 'x'
	incoming.Artifact.ID = "mutated by caller"
	if string(candidate.Messages[2].ToolCalls[0].Arguments) != `{"n":1}` || candidate.Messages[2].Artifact.ID != "new-artifact" {
		t.Fatal("new message retained caller-owned mutable fields")
	}
	editable := candidate.Clone()
	editable.Messages[1].ToolCalls[0].Arguments[0] = 'x'
	editable.Messages[1].Artifact.ID = "changed"
	if string(s.Messages[1].ToolCalls[0].Arguments) != "{}" || candidate.Messages[1].Artifact.ID != "old-artifact" {
		t.Fatal("editable clone changed shared history")
	}
}

var appendContextSink RequestContext

func BenchmarkAppendContextPreparation(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", count), func(b *testing.B) {
			s := NewSession("bench")
			s.UpsertSysMessage("system")
			args := json.RawMessage(`{"value":"` + strings.Repeat("x", 1024) + `"}`)
			for i := 0; i < count; i++ {
				m := sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: fmt.Sprint(i), Arguments: args}}}
				if err := s.AppendMessage(&m); err != nil {
					b.Fatal(err)
				}
			}
			b.Run("deep_clone_and_snapshot", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "continue"}
					candidate := s.Clone()
					if err := candidate.AppendMessage(&msg); err != nil {
						b.Fatal(err)
					}
					appendContextSink = candidate.Snapshot()
				}
			})
			b.Run("append_candidate", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "continue"}
					candidate, err := s.WithAppendedMessage(&msg)
					if err != nil {
						b.Fatal(err)
					}
					appendContextSink = candidate.RequestContext
				}
			})
		})
	}
}

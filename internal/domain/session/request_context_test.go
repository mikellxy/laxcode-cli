package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestIdentifiersSurviveSnapshotAndResume(t *testing.T) {
	s := NewSession("ids")
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
	for i, m := range messages {
		if m.Seq != uint64(i+1) {
			t.Fatalf("seq=%d at %d", m.Seq, i)
		}
	}
	call := messages[1]
	if call.TurnID == "" || call.ToolCallGroupID == "" {
		t.Fatal("call has no IDs")
	}
	for _, i := range []int{2, 3} {
		if messages[i].TurnID != call.TurnID || messages[i].ToolCallGroupID != call.ToolCallGroupID {
			t.Fatal("parallel result lost identity")
		}
	}
	if messages[4].TurnID == call.TurnID || messages[4].ToolCallGroupID != "" || messages[0].TurnID != "" {
		t.Fatal("ordinary assistant and user identity semantics")
	}
	snapshot := s.Snapshot()
	resumed := NewSession("ids")
	if err := resumed.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Messages, resumed.Messages) {
		t.Fatal("restore changed IDs")
	}
	newMsg := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "continue"}
	if err := resumed.AppendMessage(&newMsg); err != nil {
		t.Fatal(err)
	}
	if newMsg.Seq != 6 || newMsg.TurnID == call.TurnID {
		t.Fatal("ID reused after restart")
	}
	resumed.Messages[1].ToolCalls[0].Arguments[0] = 'x'
	if string(s.Messages[1].ToolCalls[0].Arguments) != "{}" {
		t.Fatal("snapshot aliased tool arguments")
	}
}

func TestLegacyIdentifiersAreStable(t *testing.T) {
	legacy := []sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: "sys"},
		{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: "c"}}},
		{Role: sharedkernel.RoleTool, ToolCallID: "c", Content: "result"},
	}
	a, b := NewSession("legacy"), NewSession("legacy")
	a.LoadMessages(legacy)
	b.LoadMessages(legacy)
	if err := a.Snapshot().Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Snapshot(), b.Snapshot()) {
		t.Fatal("migration IDs not stable")
	}
	if a.Messages[2].ToolCallGroupID != a.Messages[1].ToolCallGroupID {
		t.Fatal("migration lost tool group")
	}
	if legacy[1].Seq != 0 {
		t.Fatal("migration changed source")
	}
}

func TestInvalidSnapshotDoesNotReplaceWorkingContext(t *testing.T) {
	s := NewSession("bad")
	s.UpsertSysMessage("keep")
	before := s.Snapshot()
	bad := before.Clone()
	bad.Version = 99
	if err := s.Restore(bad); err == nil {
		t.Fatal("accepted unsupported snapshot")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("failed restore changed session")
	}
}

func TestActiveChatLifecycleAndLegacyInference(t *testing.T) {
	s := NewSession("active")
	user := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "q"}
	candidate, err := s.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ActiveChatID != "chat-1" {
		t.Fatalf("启动对话后 ActiveChatID=%q", candidate.ActiveChatID)
	}
	if _, err := candidate.WithStartedChat(&sharedkernel.Message{Role: sharedkernel.RoleUser}); !errors.Is(err, ErrChatAlreadyActive) {
		t.Fatalf("未完成对话期间应拒绝新 user，实际 %v", err)
	}
	final := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "done"}
	if err := candidate.AppendMessage(&final); err != nil {
		t.Fatal(err)
	}
	if candidate.ActiveChatID != "" {
		t.Fatalf("最终 assistant 后应清除 ActiveChatID，实际 %q", candidate.ActiveChatID)
	}

	legacy := NewSession("legacy-active")
	legacySnapshot := RequestContext{
		Version: RequestContextVersion,
		LastSeq: 2,
		Messages: []sharedkernel.Message{
			{Role: sharedkernel.RoleUser, Seq: 1, Content: "old"},
			{Role: sharedkernel.RoleAssistant, Seq: 2, ToolCalls: []sharedkernel.ToolCall{{ID: "call"}}},
		},
	}
	if err := legacy.Restore(legacySnapshot); err != nil {
		t.Fatal(err)
	}
	if legacy.ActiveChatID != "chat-1" {
		t.Fatalf("旧快照未推断出活跃对话：%q", legacy.ActiveChatID)
	}
}

func TestAppendCandidateIsolatesSliceAndIncomingMessage(t *testing.T) {
	s := NewSession("append")
	old := sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		ToolCalls: []sharedkernel.ToolCall{{ID: "old", Arguments: json.RawMessage(`{}`)}},
		Artifact:  &sharedkernel.ArtifactRef{ID: "old-artifact"},
	}
	if err := s.AppendMessage(&old); err != nil {
		t.Fatal(err)
	}
	// 原切片即使还有容量，构造候选也不能写入它的 backing array。
	backing := make([]sharedkernel.Message, 4)
	copy(backing, s.Messages)
	backing[1].Content = "unused capacity sentinel"
	s.Messages = backing[:1]
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
	if !reflect.DeepEqual(before, s.Snapshot()) || backing[1].Content != "unused capacity sentinel" {
		t.Fatal("append candidate changed the original session or its backing array")
	}
	if candidate.LastSeq != s.LastSeq+1 || candidate.TokenUsed.TokenInput != 100 {
		t.Fatal("candidate state was not advanced")
	}
	candidate.Messages[0].Content = "candidate only"
	if s.Messages[0].Content != "" {
		t.Fatal("message slice is shared")
	}
	incoming.ToolCalls[0].Arguments[0] = 'x'
	incoming.Artifact.ID = "mutated by caller"
	if string(candidate.Messages[1].ToolCalls[0].Arguments) != `{"n":1}` || candidate.Messages[1].Artifact.ID != "new-artifact" {
		t.Fatal("new message retained mutable caller-owned fields")
	}
	// 历史修改路径仍通过完整 Clone 隔离已有的嵌套字段。
	editable := candidate.Clone()
	editable.Messages[0].ToolCalls[0].Arguments[0] = 'x'
	editable.Messages[0].Artifact.ID = "changed"
	if string(s.Messages[0].ToolCalls[0].Arguments) != "{}" || candidate.Messages[0].Artifact.ID != "old-artifact" {
		t.Fatal("editable clone changed shared read-only history")
	}
}

var appendContextSink RequestContext

// 比较旧的“两次全量复制”与追加候选的内存准备开销，不包含 JSON 和磁盘 I/O。
func BenchmarkAppendContextPreparation(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", count), func(b *testing.B) {
			s := NewSession("bench")
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

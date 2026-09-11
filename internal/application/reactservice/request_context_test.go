package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func TestCompactionSnapshotAndArtifactSurviveRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	historyRoot := filepath.Join(root, ".session")
	repo, err := sessionrepo.NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), historyRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	artifacts := artifactstore.New(historyRoot)
	s := session.NewSession("resume")
	llm := &scriptedLLM{budget: llmprovider.ContextBudget{ContextWindow: 100, ReservedOutputTokens: 10}, countFn: func(msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
		if strings.Contains(msgs[2].Content, "read_artifact") {
			return 50, nil
		}
		return 80, nil
	}}
	reg := tools.NewDefaultRegistry(nil)
	svc := NewReActService(s, repo, llm, nil, reg, nil, nil, artifacts)
	if err := svc.InitSysPrompt(ctx, "sys"); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("raw 中文🙂 output\n", 1000)
	for _, id := range []string{"old", "a", "b", "c"} {
		if err := svc.handleTurnMsg(ctx, assistantMsgWithTool(sharedkernel.ToolCall{ID: id, Name: "tool", Arguments: json.RawMessage(`{}`)})); err != nil {
			t.Fatal(err)
		}
		output := "recent"
		if id == "old" {
			output = large
		}
		if err := svc.handleTurnMsg(ctx, &sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: output}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.compactContext(ctx, reg.GetAvailableTools()); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if before.Messages[2].Artifact == nil {
		t.Fatal("no artifact reference")
	}
	resumed := NewReActService(session.NewSession(s.ID), repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil, artifacts)
	if err := resumed.InitSession(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, resumed.Session.Snapshot()) {
		t.Fatal("restart changed compacted request context")
	}
	rawData, err := os.ReadFile(filepath.Join(historyRoot, s.ID, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []sharedkernel.Message
	for _, line := range strings.Split(strings.TrimSpace(string(rawData)), "\n") {
		var msg sharedkernel.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		raw = append(raw, msg)
	}
	if raw[2].Content != large || raw[2].Artifact != nil {
		t.Fatal("raw archive was modified")
	}
	args, _ := json.Marshal(map[string]any{"artifact_id": before.Messages[2].Artifact.ID, "offset": 0, "limit": 100})
	result := resumed.ToolRegistry.Execute(ctx, &sharedkernel.ToolCall{ID: "read", Name: "read_artifact", Arguments: args})
	if result.IsError || !strings.Contains(result.Output, "raw 中文🙂 output") {
		t.Fatalf("artifact read after restart failed: %+v", result)
	}
	next := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "continue"}
	if err := resumed.handleTurnMsg(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if next.Seq != before.LastSeq+1 {
		t.Fatal("sequence reused after restart")
	}
}

func TestFailedCompactionNeverReplacesCurrentContext(t *testing.T) {
	for _, failure := range []string{"artifact", "count", "target", "snapshot"} {
		t.Run(failure, func(t *testing.T) {
			repo := newMemRepo()
			s := newTestSession("failure", repo)
			svc := NewReActService(s, repo, &scriptedLLM{}, nil, tools.NewDefaultRegistry(nil), nil, nil, repo)
			for _, id := range []string{"old", "a", "b", "c"} {
				if err := svc.handleTurnMsg(context.Background(), assistantMsgWithTool(sharedkernel.ToolCall{ID: id, Name: "tool"})); err != nil {
					t.Fatal(err)
				}
				if err := svc.handleTurnMsg(context.Background(), &sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: strings.Repeat("result", 1000)}); err != nil {
					t.Fatal(err)
				}
			}
			llm := &scriptedLLM{budget: llmprovider.ContextBudget{ContextWindow: 100, ReservedOutputTokens: 10}, countFn: func(msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
				if strings.Contains(msgs[2].Content, "read_artifact") {
					if failure == "count" {
						return 0, errRepo
					}
					if failure == "target" {
						return 70, nil
					}
					return 50, nil
				}
				return 80, nil
			}}
			svc.LLMClient = llm
			before := s.Snapshot()
			repo.failArtifact = failure == "artifact"
			repo.failSnapshot = failure == "snapshot"
			err := svc.compactContext(context.Background(), svc.ToolRegistry.GetAvailableTools())
			if err == nil {
				t.Fatal("expected error")
			}
			if failure == "target" && !errors.Is(err, ErrContextTargetNotReach) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("failed compaction mutated active context")
			}
		})
	}
}

func TestAppendCommitFailurePreservesContextAndRetryIdentity(t *testing.T) {
	repo := newMemRepo()
	s := newTestSession("append-fail", repo)
	svc := NewReActService(s, repo, &scriptedLLM{}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	ctx := context.Background()
	if err := svc.handleTurnMsg(ctx, assistantMsgWithTool(sharedkernel.ToolCall{ID: "old", Arguments: json.RawMessage(`{}`)})); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	msg := sharedkernel.Message{
		Role: sharedkernel.RoleTool, ToolCallID: "old", Content: "result",
		Artifact: &sharedkernel.ArtifactRef{ID: "incoming"},
	}
	repo.failAppend = true
	if err := svc.handleTurnMsg(ctx, &msg); !errors.Is(err, errRepo) {
		t.Fatal("expected commit failure", err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) || !reflect.DeepEqual(before, repo.contexts[s.ID]) {
		t.Fatal("failed append changed committed state")
	}
	seq, originalSeq := msg.Seq, append([]uint64(nil), msg.OriginalSeq...)
	repo.failAppend = false
	if err := svc.handleTurnMsg(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Seq != seq || !reflect.DeepEqual(msg.OriginalSeq, originalSeq) {
		t.Fatal("retry changed identity")
	}
	msg.Artifact.ID = "caller edit"
	if s.Messages[len(s.Messages)-1].Artifact.ID != "incoming" {
		t.Fatal("caller mutation reached active context")
	}
	s.Messages[len(s.Messages)-1].Artifact.ID = "session edit"
	if repo.contexts[s.ID].Messages[len(s.Messages)-1].Artifact.ID != "incoming" || repo.storedMsgs(s.ID)[len(repo.storedMsgs(s.ID))-1].Artifact.ID != "incoming" {
		t.Fatal("repository retained borrowed mutable references")
	}
}

package sessionrepo

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func newTestRepo(t *testing.T) (*SqliteSessionRepo, string) {
	t.Helper()
	root := t.TempDir()
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, ".session"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, filepath.Join(root, ".session")
}

func saveSnapshot(t *testing.T, repo *SqliteSessionRepo, s *session.Session) {
	t.Helper()
	revision, err := repo.CommitSnapshot(context.Background(), s.ID, s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	s.Revision = revision
}

func commitAppendedMessage(
	t *testing.T,
	repo *SqliteSessionRepo,
	s *session.Session,
	msg sharedkernel.Message,
) {
	t.Helper()
	revision, err := repo.CommitAppendedMessage(context.Background(), s.ID, s.Snapshot(), msg)
	if err != nil {
		t.Fatal(err)
	}
	s.Revision = revision
}

func TestSqliteSessionRepoPersistsHistoryAndCompactedVariants(t *testing.T) {
	repo, historyRoot := newTestRepo(t)
	s := session.NewSession("s1")
	s.UpsertSysMessage("system")
	saveSnapshot(t, repo, s)

	user := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "question"}
	started, err := s.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	s = started
	commitAppendedMessage(t, repo, s, user)
	original := sharedkernel.Message{
		Role: sharedkernel.RoleAssistant, Content: "a very long original answer",
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 120, TokenOutput: 20},
	}
	if err := s.AppendMessage(&original); err != nil {
		t.Fatal(err)
	}
	commitAppendedMessage(t, repo, s, original)

	s.Messages[2].Content = "short answer"
	s.Messages[2].Artifact = &sharedkernel.ArtifactRef{ID: "abc", ByteSize: 27}
	saveSnapshot(t, repo, s)

	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s.Snapshot()) {
		t.Fatalf("restored context differs:\n got=%+v\nwant=%+v", got, s.Snapshot())
	}

	var originalCount, compactedCount, historyCount, contextCount int64
	if err := repo.db.Model(&messageModel{}).Where("session_id = ? AND message_kind = ?", s.ID, messageKindOriginal).Count(&originalCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&messageModel{}).Where("session_id = ? AND message_kind = ?", s.ID, messageKindCompacted).Count(&compactedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&historyEntryModel{}).Where("session_id = ?", s.ID).Count(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&sessionContextMessageModel{}).Where("session_id = ?", s.ID).Count(&contextCount).Error; err != nil {
		t.Fatal(err)
	}
	if originalCount != 2 || compactedCount != 1 || historyCount != 2 || contextCount != 3 {
		t.Fatalf("unexpected row counts: original=%d compacted=%d history=%d context=%d",
			originalCount, compactedCount, historyCount, contextCount)
	}

	f, err := os.Open(filepath.Join(historyRoot, s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var backup []sharedkernel.Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var msg sharedkernel.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		backup = append(backup, msg)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(backup) != 2 || backup[1].Content != original.Content {
		t.Fatalf("history backup must retain original messages: %+v", backup)
	}
}

func TestSqliteSessionRepoRejectsStaleRevision(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := session.NewSession("conflict")
	s.UpsertSysMessage("one")
	saveSnapshot(t, repo, s)
	stale := s.Snapshot()

	s.UpsertSysMessage("two")
	saveSnapshot(t, repo, s)
	stale.Messages[0].Content = "stale"
	if _, err := repo.CommitSnapshot(context.Background(), s.ID, stale); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func TestCommitSnapshotEnforcesSequence(t *testing.T) {
	repo, historyRoot := newTestRepo(t)
	s := session.NewSession("snapshot-sequence")
	s.UpsertSysMessage("system")
	saveSnapshot(t, repo, s)

	if _, err := os.Stat(filepath.Join(historyRoot, s.ID, historyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot commit must not create history backup, stat err=%v", err)
	}

	user := s.BuildUserMessage("question")
	grown, err := s.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitSnapshot(context.Background(), s.ID, grown.Snapshot()); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("snapshot accepted LastSeq growth: %v", err)
	}

	s = grown
	commitAppendedMessage(t, repo, s, user)
	rolledBack := s.Snapshot()
	rolledBack.LastSeq = 0
	rolledBack.Messages = rolledBack.Messages[:1]
	if _, err := repo.CommitSnapshot(context.Background(), s.ID, rolledBack); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("snapshot accepted LastSeq rollback: %v", err)
	}

	newSession := session.NewSession("snapshot-import")
	imported := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "not an initializer"}
	if err := newSession.AppendMessage(&imported); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitSnapshot(context.Background(), newSession.ID, newSession.Snapshot()); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("snapshot accepted initial history import: %v", err)
	}
}

func TestCommitAppendedMessageRejectsInvalidIntent(t *testing.T) {
	type mutation func(snapshot *session.RequestContext, msg *sharedkernel.Message)
	tests := []struct {
		name   string
		mutate mutation
	}{
		{
			name: "stale revision",
			mutate: func(snapshot *session.RequestContext, _ *sharedkernel.Message) {
				snapshot.Revision--
			},
		},
		{
			name: "last seq is not next",
			mutate: func(snapshot *session.RequestContext, _ *sharedkernel.Message) {
				snapshot.LastSeq++
			},
		},
		{
			name: "missing snapshot tail",
			mutate: func(snapshot *session.RequestContext, _ *sharedkernel.Message) {
				snapshot.Messages = nil
			},
		},
		{
			name: "system message",
			mutate: func(snapshot *session.RequestContext, msg *sharedkernel.Message) {
				*msg = sharedkernel.Message{Role: sharedkernel.RoleSystem}
				snapshot.Messages = []sharedkernel.Message{msg.Clone()}
			},
		},
		{
			name: "message seq mismatch",
			mutate: func(_ *session.RequestContext, msg *sharedkernel.Message) {
				msg.Seq++
			},
		},
		{
			name: "message content mismatch",
			mutate: func(_ *session.RequestContext, msg *sharedkernel.Message) {
				msg.Content = "different"
			},
		},
		{
			name: "non-final message without active chat",
			mutate: func(snapshot *session.RequestContext, _ *sharedkernel.Message) {
				snapshot.ActiveChatID = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _ := newTestRepo(t)
			s := session.NewSession("invalid-append")
			s.UpsertSysMessage("system")
			saveSnapshot(t, repo, s)
			msg := s.BuildUserMessage("question")
			candidate, err := s.WithStartedChat(&msg)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := candidate.Snapshot()
			tt.mutate(&snapshot, &msg)
			if _, err := repo.CommitAppendedMessage(context.Background(), s.ID, snapshot, msg); err == nil {
				t.Fatal("invalid append was accepted")
			}
			got, err := repo.GetRequestContext(context.Background(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, s.Snapshot()) {
				t.Fatalf("rejected append changed stored context: got=%+v want=%+v", got, s.Snapshot())
			}
		})
	}

	repo, _ := newTestRepo(t)
	missing := session.NewSession("missing-session")
	msg := missing.BuildUserMessage("question")
	candidate, err := missing.WithStartedChat(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitAppendedMessage(context.Background(), missing.ID, candidate.Snapshot(), msg); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("append to missing session: %v", err)
	}

	repo, _ = newTestRepo(t)
	s := session.NewSession("final-active")
	s.UpsertSysMessage("system")
	saveSnapshot(t, repo, s)
	final := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "done"}
	finalCandidate, err := s.WithAppendedMessage(&final)
	if err != nil {
		t.Fatal(err)
	}
	// 绕过 domain 手工构造异常快照：正常路径不会出现最终 assistant 携带
	// 活跃对话，此处验证仓储对绕过聚合方法的调用方的防御性拒绝。
	finalCandidate.ActiveChatID = "chat-must-be-cleared"
	if _, err := repo.CommitAppendedMessage(context.Background(), s.ID, finalCandidate.Snapshot(), final); err == nil {
		t.Fatal("final assistant with active chat was accepted")
	}
}

func TestCommitAppendedMessageKeepsHistoryChatAfterFinalAssistant(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := session.NewSession("history-chat")
	s.UpsertSysMessage("system")
	saveSnapshot(t, repo, s)

	user := s.BuildUserMessage("question")
	candidate, err := s.WithStartedChat(&user)
	if err != nil {
		t.Fatal(err)
	}
	s = candidate
	commitAppendedMessage(t, repo, s, user)

	toolCall := sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		ToolCalls: []sharedkernel.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
	}
	candidate, err = s.WithAppendedMessage(&toolCall)
	if err != nil {
		t.Fatal(err)
	}
	invalidNoChat := candidate.Snapshot()
	invalidNoChat.ActiveChatID = ""
	if _, err := repo.CommitAppendedMessage(context.Background(), s.ID, invalidNoChat, toolCall); err == nil {
		t.Fatal("non-final assistant reused the old history chat after ActiveChatID was cleared")
	}
	s = candidate
	commitAppendedMessage(t, repo, s, toolCall)

	toolResult := sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: "call", Content: "result"}
	candidate, err = s.WithAppendedMessage(&toolResult)
	if err != nil {
		t.Fatal(err)
	}
	s = candidate
	commitAppendedMessage(t, repo, s, toolResult)

	final := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "done"}
	candidate, err = s.WithAppendedMessage(&final)
	if err != nil {
		t.Fatal(err)
	}
	s = candidate
	commitAppendedMessage(t, repo, s, final)

	var entries []historyEntryModel
	if err := repo.db.Where("session_id = ?", s.ID).Order("seq ASC").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("history entries=%d, want 4", len(entries))
	}
	for _, entry := range entries {
		if entry.ActiveChatID != "chat-1" {
			t.Fatalf("seq %d history chat=%q, want chat-1", entry.Seq, entry.ActiveChatID)
		}
	}
	if s.ActiveChatID != "" {
		t.Fatalf("final assistant left active chat %q", s.ActiveChatID)
	}
}

func TestCommitAppendedMessageRollsBackAllDatabaseWrites(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := session.NewSession("rollback")
	s.UpsertSysMessage("system")
	saveSnapshot(t, repo, s)
	before := s.Snapshot()
	if err := repo.db.Exec(`CREATE TRIGGER fail_history_insert
		BEFORE INSERT ON history_entries
		BEGIN SELECT RAISE(ABORT, 'forced history failure'); END`).Error; err != nil {
		t.Fatal(err)
	}

	msg := s.BuildUserMessage("question")
	candidate, err := s.WithStartedChat(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitAppendedMessage(context.Background(), s.ID, candidate.Snapshot(), msg); err == nil {
		t.Fatal("expected forced history failure")
	}

	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("failed transaction changed context: got=%+v want=%+v", got, before)
	}
	var historyCount int64
	if err := repo.db.Model(&historyEntryModel{}).Where("session_id = ?", s.ID).Count(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if historyCount != 0 {
		t.Fatalf("failed transaction left %d history rows", historyCount)
	}
}

func TestHistoryBackupFailureDoesNotFailDatabaseCommit(t *testing.T) {
	root := t.TempDir()
	badHistoryRoot := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(badHistoryRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), badHistoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	s := session.NewSession("backup-failure")
	s.UpsertSysMessage("sys")
	saveSnapshot(t, repo, s)
	msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "persist me"}
	started, err := s.WithStartedChat(&msg)
	if err != nil {
		t.Fatal(err)
	}
	s = started
	commitAppendedMessage(t, repo, s, msg)
	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil || len(got.Messages) != 2 || got.Messages[1].Content != "persist me" {
		t.Fatalf("database commit was lost: context=%+v err=%v", got, err)
	}
}

func TestSchemaCreatesForeignKeysAndAnalysisIndexes(t *testing.T) {
	repo, _ := newTestRepo(t)
	for table, want := range map[string]int{
		"messages": 2, "session_context_messages": 2, "history_entries": 2,
	} {
		var rows []struct{ ID int }
		if err := repo.db.Raw("PRAGMA foreign_key_list(" + table + ")").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Errorf("%s foreign keys=%d, want %d", table, len(rows), want)
		}
	}
	for _, name := range []string{
		"idx_messages_session_role",
		"idx_messages_session_turn",
		"idx_messages_tool_group",
		"idx_messages_origin",
	} {
		var count int64
		if err := repo.db.Raw(
			"SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name,
		).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("missing analysis index %s", name)
		}
	}
}

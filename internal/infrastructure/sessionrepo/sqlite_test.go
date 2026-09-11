package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func newTestRepo(t *testing.T) (*SqliteSessionRepo, string) {
	t.Helper()
	root := t.TempDir()
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, root
}

func createSystem(t *testing.T, repo *SqliteSessionRepo, id, content string) *session.Session {
	t.Helper()
	s := session.NewSession(id)
	sys := s.UpsertSysMessage(content)
	revision, err := repo.CommitCreateMessage(context.Background(), id, s.Snapshot(), sys, sys)
	if err != nil {
		t.Fatal(err)
	}
	s.Revision = revision
	return s
}

func appendMessage(t *testing.T, repo *SqliteSessionRepo, s *session.Session, msg *sharedkernel.Message) {
	t.Helper()
	candidate, err := s.WithAppendedMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitCreateMessage(context.Background(), s.ID, candidate.Snapshot(), *msg, *msg)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	*s = *candidate
}

func TestTwoTableLifecycleAndGenerationHistory(t *testing.T) {
	repo, root := newTestRepo(t)
	s := createSystem(t, repo, "lifecycle", "system-v1")
	user := s.BuildUserMessage("raw question")
	appendMessage(t, repo, s, &user)

	compressed := s.Clone()
	compressed.Messages[1].Content = "compressed question"
	if err := compressed.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, compressed.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	compressed.Revision = revision
	*s = *compressed

	updated := s.Clone()
	sys := updated.UpsertSysMessage("system-v2")
	revision, err = repo.CommitUpdateMessage(context.Background(), s.ID, updated.Snapshot(), sys)
	if err != nil {
		t.Fatal(err)
	}
	updated.Revision = revision
	*s = *updated

	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s.Snapshot()) {
		t.Fatalf("restored context differs: got=%+v want=%+v", got, s.Snapshot())
	}
	if got.MemoryGeneration != 2 || got.LastSeq != 2 || got.Messages[0].Content != "system-v2" || got.Messages[1].Content != "compressed question" {
		t.Fatalf("unexpected current context: %+v", got)
	}

	var originals, firstGeneration, secondGeneration []messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ?", s.ID, messageTypeOriginal).Order("seq").Find(&originals).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = 1", s.ID, messageTypeMemory).Order("seq").Find(&firstGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = 2", s.ID, messageTypeMemory).Order("seq").Find(&secondGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if len(originals) != 2 || originals[0].Content != "system-v1" || originals[1].Content != "raw question" {
		t.Fatalf("original history changed: %+v", originals)
	}
	if len(firstGeneration) != 2 || firstGeneration[0].Content != "system-v1" || firstGeneration[1].Content != "raw question" {
		t.Fatalf("sealed first generation changed: %+v", firstGeneration)
	}
	if len(secondGeneration) != 2 || secondGeneration[0].Content != "system-v2" || secondGeneration[1].Content != "compressed question" {
		t.Fatalf("unexpected second generation: %+v", secondGeneration)
	}

	raw, err := os.ReadFile(filepath.Join(root, "history", s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	var history []sharedkernel.Message
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var msg sharedkernel.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		history = append(history, msg)
	}
	if len(history) != 2 || history[0].Content != "system-v1" || history[1].Content != "raw question" {
		t.Fatalf("unexpected JSONL history: %+v", history)
	}
}

func TestSummaryOriginalSequencesRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "summary-origins", "system")
	for _, msg := range []*sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "question one"},
		{Role: sharedkernel.RoleAssistant, Content: "answer one"},
		{Role: sharedkernel.RoleUser, Content: "current question"},
	} {
		appendMessage(t, repo, s, msg)
	}
	candidate := s.Clone()
	merged, err := compactor.MergeSummary(candidate.Messages, 3, `{"objective":"current question"}`)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Messages = merged
	if err := candidate.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, candidate.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	*s = *candidate

	loaded, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, s.Snapshot()) ||
		!reflect.DeepEqual(loaded.Messages[1].OriginalSeq, []uint64{2, 3}) {
		t.Fatalf("summary origins did not round trip: %+v", loaded)
	}
	var row messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = ? AND seq = ?",
		s.ID, messageTypeMemory, 2, 2).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if string(row.OriginalSeqJSON) != "[2,3]" {
		t.Fatalf("unexpected persisted original sequence JSON: %s", row.OriginalSeqJSON)
	}
}

func TestSchemaContainsOnlyTwoBusinessTables(t *testing.T) {
	repo, _ := newTestRepo(t)
	var names []string
	if err := repo.db.Raw(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"messages", "request_contexts"}) {
		t.Fatalf("tables=%v", names)
	}
}

func TestCreateRejectsStaleRevisionAndSequence(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "conflict", "system")
	before, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg := s.BuildUserMessage("question")
	candidate, err := s.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	stale := candidate.Snapshot()
	stale.Revision--
	if _, err := repo.CommitCreateMessage(context.Background(), s.ID, stale, msg, msg); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	wrong := msg.Clone()
	wrong.Seq++
	wrong.OriginalSeq = []uint64{wrong.Seq}
	if _, err := repo.CommitCreateMessage(context.Background(), s.ID, candidate.Snapshot(), wrong, wrong); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("stale sequence: %v", err)
	}
	after, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected writes changed context")
	}
}

func TestUpdateChangesOnlyCurrentMemory(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "update", "old")
	candidate := s.Clone()
	sys := candidate.UpsertSysMessage("new")
	revision, err := repo.CommitUpdateMessage(context.Background(), s.ID, candidate.Snapshot(), sys)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	var originals []messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ?", s.ID, messageTypeOriginal).Find(&originals).Error; err != nil {
		t.Fatal(err)
	}
	if len(originals) != 1 || originals[0].Content != "old" {
		t.Fatalf("system update changed original: %+v", originals)
	}
	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryGeneration != 1 || got.LastSeq != 1 || got.Messages[0].Content != "new" {
		t.Fatalf("unexpected updated context: %+v", got)
	}
}

func TestGenerationFailureRollsBackContextHeadAndRows(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "rollback", "system")
	before := s.Snapshot()
	if err := repo.db.Exec(`CREATE TRIGGER fail_generation BEFORE INSERT ON messages
		WHEN NEW.memory_generation = 2
		BEGIN SELECT RAISE(ABORT, 'forced generation failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	candidate := s.Clone()
	candidate.Messages[0].Content = "compressed"
	if err := candidate.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, candidate.Snapshot()); err == nil {
		t.Fatal("expected generation failure")
	}
	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("failed generation changed context: %+v", got)
	}
	var count int64
	if err := repo.db.Model(&messageModel{}).Where("session_id = ? AND memory_generation = 2", s.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed generation left %d rows", count)
	}
}

package sessionrepo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestSnapshotRestoresCompactedContextWithoutReadingHistory(t *testing.T) {
	ctx := context.Background()
	r := NewFsSessionRepo(t.TempDir())
	s := session.NewSession("snapshot")
	s.UpsertSysMessage("sys")
	m := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: strings.Repeat("raw", 1000)}
	if err := s.AppendMessage(&m); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRequestContext(ctx, s.ID, s.Snapshot(), &m); err != nil {
		t.Fatal(err)
	}
	s.Messages[1].Content = "shortened"
	if err := r.SaveRequestContext(ctx, s.ID, s.Snapshot(), nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(r.Dir, s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	var stored sharedkernel.Message
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Content != m.Content || stored.Seq != m.Seq {
		t.Fatal("raw history was rewritten")
	}
	// 让 history 变成目录：新格式恢复必须完全不依赖原文读取。
	history := filepath.Join(r.Dir, s.ID, historyFile)
	if err := os.Rename(history, history+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(history, 0o700); err != nil {
		t.Fatal(err)
	}
	restored, err := r.GetRequestContext(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, s.Snapshot()) {
		t.Fatal("restored raw history instead of snapshot")
	}
}

func TestPendingCommitRecoveryIsIdempotent(t *testing.T) {
	for _, phase := range []string{"intent", "partial_history", "history", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			r := NewFsSessionRepo(t.TempDir())
			s := session.NewSession("recover")
			first := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "q"}
			if err := s.AppendMessage(&first); err != nil {
				t.Fatal(err)
			}
			if err := r.SaveRequestContext(ctx, s.ID, s.Snapshot(), &first); err != nil {
				t.Fatal(err)
			}
			history := filepath.Join(r.Dir, s.ID, historyFile)
			before, err := os.ReadFile(history)
			if err != nil {
				t.Fatal(err)
			}
			m := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "中文🙂 result"}
			if err := s.AppendMessage(&m); err != nil {
				t.Fatal(err)
			}
			pending := pendingContext{Snapshot: s.Snapshot(), Original: &m, HistoryOffset: int64(len(before))}
			data, _ := json.Marshal(pending)
			if err := r.writeFile(ctx, filepath.Join(r.Dir, s.ID, pendingContextFile), data); err != nil {
				t.Fatal(err)
			}
			line, _ := json.Marshal(m)
			line = append(line, '\n')
			if phase != "intent" {
				piece := line
				if phase == "partial_history" {
					piece = line[:len(line)/2]
				}
				if err := os.WriteFile(history, append(append([]byte(nil), before...), piece...), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "snapshot" {
				data, _ = json.Marshal(s.Snapshot())
				if err := r.writeFile(ctx, filepath.Join(r.Dir, s.ID, requestContextFile), data); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				got, err := r.GetRequestContext(ctx, s.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, s.Snapshot()) {
					t.Fatal("recovery lost snapshot")
				}
			}
			after, err := os.ReadFile(history)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before)+string(line) {
				t.Fatal("recovery duplicated or lost history bytes")
			}
			if _, err := os.Stat(filepath.Join(r.Dir, s.ID, pendingContextFile)); !os.IsNotExist(err) {
				t.Fatal("pending commit not cleared")
			}
		})
	}
}

func TestLegacyMigrationOnceAndCorruptSnapshotFailsClosed(t *testing.T) {
	ctx := context.Background()
	r := NewFsSessionRepo(t.TempDir())
	id := "legacy"
	if err := r.AppendMessage(ctx, id, &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "old"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetRequestContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeq != 1 || got.Messages[0].Seq != 1 {
		t.Fatal("missing migration IDs")
	}
	if err := os.WriteFile(filepath.Join(r.Dir, id, historyFile), []byte("unreadable history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetRequestContext(ctx, id); err != nil {
		t.Fatal("new snapshot must not reload history", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, id, requestContextFile), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetRequestContext(ctx, id); err == nil {
		t.Fatal("corrupt snapshot silently fell back")
	}
}

func TestDuplicateCommitDoesNotAppendHistory(t *testing.T) {
	ctx := context.Background()
	r := NewFsSessionRepo(t.TempDir())
	s := session.NewSession("duplicate")
	m := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "q"}
	if err := s.AppendMessage(&m); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRequestContext(ctx, s.ID, s.Snapshot(), &m); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(r.Dir, s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRequestContext(ctx, s.ID, s.Snapshot(), &m); err == nil {
		t.Fatal("accepted duplicate Seq")
	}
	after, err := os.ReadFile(filepath.Join(r.Dir, s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("duplicate commit changed raw history")
	}
}

func TestMigrationRejectsCorruptHistoryAndCancelledLoad(t *testing.T) {
	r := NewFsSessionRepo(t.TempDir())
	id := "corrupt-history"
	if err := r.AppendMessage(context.Background(), id, &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, id, historyFile), []byte("broken json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetRequestContext(context.Background(), id); err == nil {
		t.Fatal("silently skipped corrupt history")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, id, requestContextFile)); !os.IsNotExist(err) {
		t.Fatal("committed partial migration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.GetRequestContext(ctx, "cancelled"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

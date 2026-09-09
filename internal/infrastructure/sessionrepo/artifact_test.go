package sessionrepo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestArtifactToolPagesExactImmutableOutput(t *testing.T) {
	ctx := context.Background()
	r := NewFsSessionRepo(t.TempDir())
	content := strings.Repeat("中文🙂\n", 2500) + "no final newline"
	ref, err := r.PutArtifact(ctx, "artifacts", content)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.PutArtifact(ctx, "artifacts", content)
	if err != nil || again != ref {
		t.Fatal("content IDs must be stable", err)
	}
	tool := tools.NewReadArtifactTool(r, "artifacts")
	var restored strings.Builder
	for offset := 0; ; {
		args, _ := json.Marshal(map[string]any{"artifact_id": ref.ID, "offset": offset, "limit": 3001})
		out, err := tool.Execute(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		var page tools.ArtifactPage
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(page.Content) || utf8.RuneCountInString(page.Content) > 3001 {
			t.Fatal("invalid/big page")
		}
		restored.WriteString(page.Content)
		if page.EOF {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("pagination made no progress")
		}
		offset = page.NextOffset
	}
	if restored.String() != content || ref.ByteSize != len(content) {
		t.Fatal("artifact changed original content")
	}
	if _, err := r.ReadArtifact(ctx, "another-session", ref.ID, 0, 20); err == nil {
		t.Fatal("read crossed session boundary")
	}
	for _, id := range []string{"../request_context.json", strings.Repeat("z", 64), ""} {
		if _, err := r.ReadArtifact(ctx, "artifacts", id, 0, 20); err == nil {
			t.Fatal("accepted invalid ID")
		}
	}
	if _, err := r.ReadArtifact(ctx, "artifacts", ref.ID, 0, 4001); err == nil {
		t.Fatal("unbounded read")
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "artifacts", "artifacts", ref.ID), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadArtifact(ctx, "artifacts", ref.ID, 0, 20); err == nil {
		t.Fatal("read corrupted artifact")
	}
	if _, err := r.PutArtifact(ctx, "artifacts", content); err == nil {
		t.Fatal("silently replaced corrupt artifact")
	}
}

func TestArtifactReadRejectsSymlinkOutsideArtifactDirectory(t *testing.T) {
	ctx := context.Background()
	r := NewFsSessionRepo(t.TempDir())
	ref, err := r.PutArtifact(ctx, "source", "private output")
	if err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(r.Dir, "target", "artifacts")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(r.Dir, "source", "artifacts", ref.ID), filepath.Join(targetDir, ref.ID)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := r.ReadArtifact(ctx, "target", ref.ID, 0, 20); err == nil {
		t.Fatal("read escaped artifact directory through symlink")
	}
}

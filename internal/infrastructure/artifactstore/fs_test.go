package artifactstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreRoundTripAndPaging(t *testing.T) {
	store := New(t.TempDir())
	content := "你好🙂 artifact"
	ref, err := store.PutArtifact(context.Background(), "s1", content)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadArtifact(context.Background(), "s1", ref.ID, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content != "你好🙂" || page.EOF {
		t.Fatalf("unexpected page: %+v", page)
	}
	rest, err := store.ReadArtifact(context.Background(), "s1", ref.ID, page.NextOffset, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content+rest.Content != content || !rest.EOF {
		t.Fatalf("round trip failed: first=%+v rest=%+v", page, rest)
	}
}

func TestFileStoreRejectsInvalidIDsAndCorruption(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	for _, id := range []string{"../escape", "", strings.Repeat("z", 64)} {
		if _, err := store.ReadArtifact(context.Background(), "s1", id, 0, 1); err == nil {
			t.Fatalf("accepted invalid artifact id %q", id)
		}
	}
	ref, err := store.PutArtifact(context.Background(), "s1", "content")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "s1", "artifacts", ref.ID), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadArtifact(context.Background(), "s1", ref.ID, 0, 10); err == nil {
		t.Fatal("corrupt artifact was accepted")
	}
}

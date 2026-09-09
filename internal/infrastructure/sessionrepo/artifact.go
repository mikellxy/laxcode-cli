package sessionrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

var _ tools.ArtifactStore = (*FsSessionRepo)(nil)

func artifactID(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (r *FsSessionRepo) PutArtifact(ctx context.Context, id, content string) (sharedkernel.ArtifactRef, error) {
	if err := validSessionID(id); err != nil {
		return sharedkernel.ArtifactRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return sharedkernel.ArtifactRef{}, err
	}
	if !utf8.ValidString(content) {
		return sharedkernel.ArtifactRef{}, fmt.Errorf("artifact content is not valid UTF-8")
	}
	ref := sharedkernel.ArtifactRef{ID: artifactID([]byte(content)), ByteSize: len(content)}
	path := filepath.Join(r.Dir, id, "artifacts", ref.ID)
	if existing, err := os.ReadFile(path); err == nil {
		if artifactID(existing) != ref.ID {
			return ref, fmt.Errorf("artifact digest mismatch: %s", ref.ID)
		}
		return ref, nil
	} else if !os.IsNotExist(err) {
		return ref, err
	}
	// writeFile 为 JSON 文档追加换行；artifact 必须逐字节保存，使用专用写入。
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ref, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "artifact-*.tmp")
	if err != nil {
		return ref, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return ref, err
	}
	if err := f.Sync(); err != nil {
		return ref, err
	}
	if err := f.Close(); err != nil {
		return ref, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return ref, err
	}
	return ref, syncDir(filepath.Dir(path))
}

func (r *FsSessionRepo) ReadArtifact(ctx context.Context, sid, id string, offset, limit int) (tools.ArtifactPage, error) {
	var page tools.ArtifactPage
	if err := validSessionID(sid); err != nil {
		return page, err
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != id {
		return page, fmt.Errorf("invalid artifact ID")
	}
	if offset < 0 || limit < 1 || limit > tools.MaxArtifactPageRunes {
		return page, fmt.Errorf("invalid artifact page range")
	}
	root, err := os.OpenRoot(filepath.Join(r.Dir, sid, "artifacts"))
	if err != nil {
		return page, fmt.Errorf("open session artifacts: %w", err)
	}
	defer root.Close()
	f, err := root.Open(id)
	if err != nil {
		return page, fmt.Errorf("read artifact %s: %w", id, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return page, fmt.Errorf("read artifact %s: %w", id, err)
	}
	if artifactID(data) != id {
		return page, fmt.Errorf("artifact digest mismatch: %s", id)
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	runes := []rune(string(data))
	if offset > len(runes) {
		return page, fmt.Errorf("artifact offset beyond end (%d)", len(runes))
	}
	end := offset + min(limit, len(runes)-offset)
	return tools.ArtifactPage{ID: id, Content: string(runes[offset:end]), Offset: offset, NextOffset: end, TotalRunes: len(runes), EOF: end == len(runes)}, nil
}

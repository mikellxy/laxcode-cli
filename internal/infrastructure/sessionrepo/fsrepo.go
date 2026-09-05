package sessionrepo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const historyFile = "history.jsonl"

const metaFile = "meta.json"

type FsSessionRepo struct {
	Dir string
}

func NewFsSessionRepo(dir string) *FsSessionRepo {
	return &FsSessionRepo{
		Dir: dir,
	}
}

func (r *FsSessionRepo) AppendMessage(ctx context.Context, sessionID string, msg *sharedkernel.Message) error {
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	path := filepath.Join(r.Dir, sessionID, historyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return nil
}

func (r *FsSessionRepo) UpdateMeta(ctx context.Context, sessionID string, meta *sharedkernel.SessionMeta) error {
	path := filepath.Join(r.Dir, sessionID, metaFile)
	dir := filepath.Dir(path)

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "meta.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
	}
	return nil
}

func (r *FsSessionRepo) GetMessages(ctx context.Context, sessionID string) ([]sharedkernel.Message, error) {
	path := filepath.Join(r.Dir, sessionID, historyFile)
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return nil, nil
	}
	defer f.Close()

	var msgs []sharedkernel.Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue // 空白行静默跳过
		}
		var msg sharedkernel.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := scanner.Err(); err != nil {
	}
	return msgs, nil
}

func (r *FsSessionRepo) GetMeta(ctx context.Context, sessionID string) (*sharedkernel.SessionMeta, error) {
	path := filepath.Join(r.Dir, sessionID, metaFile)
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return new(sharedkernel.SessionMeta), nil
	}

	var meta sharedkernel.SessionMeta
	if err := json.Unmarshal(content, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

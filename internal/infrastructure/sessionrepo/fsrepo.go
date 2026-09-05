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
	return nil
}

func (r *FsSessionRepo) UpdateMeta(ctx context.Context, sessionID string, msg *sharedkernel.SessionMeta) error {
	return nil
}

func (r *FsSessionRepo) GetMessages(ctx context.Context, sessionID string) ([]sharedkernel.Message, error) {
	path := filepath.Join(r.Dir, historyFile)
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
	path := filepath.Join(r.Dir, metaFile)
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

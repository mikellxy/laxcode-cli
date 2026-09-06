package sessionrepo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const historyFile = "history.jsonl"

const metaFile = "meta.json"

const sysMessageFile = "sys_message.json"

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

func (r *FsSessionRepo) UpsertSysMessage(ctx context.Context, sessionID string, msg *sharedkernel.Message) error {
	path := filepath.Join(r.Dir, sessionID, sysMessageFile)

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return err
	}

	return r.writeFile(ctx, path, data)
}

func (r *FsSessionRepo) UpdateMeta(ctx context.Context, sessionID string, meta *sharedkernel.SessionMeta) error {
	path := filepath.Join(r.Dir, sessionID, metaFile)

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	return r.writeFile(ctx, path, data)
}

func (r *FsSessionRepo) writeFile(ctx context.Context, path string, data []byte) error {
	dir := filepath.Dir(path)

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
	var msgs []sharedkernel.Message
	{
		path := filepath.Join(r.Dir, sessionID, sysMessageFile)
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if len(data) > 0 {
			var sysMsg sharedkernel.Message
			if err = json.Unmarshal(data, &sysMsg); err != nil {
				return nil, err
			}
			msgs = append(msgs, sysMsg)
		}
	}

	path := filepath.Join(r.Dir, sessionID, historyFile)
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return msgs, nil
	}
	defer f.Close()

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

func (r *FsSessionRepo) GetMeta(ctx context.Context, sessionID string) (sharedkernel.SessionMeta, error) {
	var meta sharedkernel.SessionMeta
	path := filepath.Join(r.Dir, sessionID, metaFile)
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return meta, err
		}
		return meta, nil
	}

	if err := json.Unmarshal(content, &meta); err != nil {
		return meta, err
	}

	return meta, nil
}

var _ session.SessionRepository = (*FsSessionRepo)(nil)

package sessionrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const requestContextFile = "request_context.json"
const pendingContextFile = "request_context.pending.json"

// 保存提交意图后才追加流水。恢复使用已知偏移验证/补齐最后一条原文，
// 不扫描或重新加载历史；进程中断发生在任意步骤都不会重复追加消息。
type pendingContext struct {
	Snapshot      session.RequestContext `json:"snapshot"`
	Original      *sharedkernel.Message  `json:"original,omitempty"`
	HistoryOffset int64                  `json:"history_offset"`
}

func validSessionID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid session ID")
	}
	return nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (r *FsSessionRepo) GetRequestContext(ctx context.Context, id string) (session.RequestContext, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validSessionID(id); err != nil {
		return session.RequestContext{}, err
	}
	if err := ctx.Err(); err != nil {
		return session.RequestContext{}, err
	}
	if err := r.recoverContext(ctx, id); err != nil {
		return session.RequestContext{}, err
	}
	path := filepath.Join(r.Dir, id, requestContextFile)
	data, err := os.ReadFile(path)
	if err == nil {
		var snapshot session.RequestContext
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return snapshot, fmt.Errorf("read request context: %w", err)
		}
		return snapshot, snapshot.Validate()
	}
	if !os.IsNotExist(err) {
		return session.RequestContext{}, err
	}
	// 唯一兼容分支：没有快照的旧会话迁移一次。损坏快照不会走此分支。
	msgs, err := r.getMessages(ctx, id, true)
	if err != nil {
		return session.RequestContext{}, err
	}
	meta, err := r.GetMeta(ctx, id)
	if err != nil {
		return session.RequestContext{}, err
	}
	s := session.NewSession(id)
	s.LoadMessages(msgs)
	s.LoadMeta(meta)
	snapshot := s.Snapshot()
	if err := snapshot.Validate(); err != nil {
		return snapshot, err
	}
	if len(msgs) > 0 {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return snapshot, err
		}
		if err := r.writeFile(ctx, path, data); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func (r *FsSessionRepo) SaveRequestContext(ctx context.Context, id string, snapshot session.RequestContext, original *sharedkernel.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validSessionID(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	// 拒绝重复提交或来自旧会话副本的追加，避免 I/O 结果不确定时复用 Seq。
	data, err := os.ReadFile(filepath.Join(r.Dir, id, requestContextFile))
	if err == nil {
		var previous session.RequestContext
		if err := json.Unmarshal(data, &previous); err != nil {
			return err
		}
		if err := previous.Validate(); err != nil {
			return err
		}
		expected := previous.LastSeq
		if original != nil {
			expected++
		}
		if snapshot.LastSeq != expected {
			return fmt.Errorf("stale request context: last_seq=%d expected=%d; reload before writing", snapshot.LastSeq, expected)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// 未完成提交的结果可能已经写盘，禁止旧内存再次写入。调用者须重新加载。
	if _, err := os.Stat(filepath.Join(r.Dir, id, pendingContextFile)); err == nil {
		return fmt.Errorf("session has a pending commit; reload request context before writing")
	} else if !os.IsNotExist(err) {
		return err
	}
	pending := pendingContext{Snapshot: snapshot, Original: original}
	if original != nil {
		if original.Role == sharedkernel.RoleSystem || original.Seq == 0 || original.Seq != snapshot.LastSeq {
			return fmt.Errorf("original message does not match request context")
		}
		if len(snapshot.Messages) == 0 {
			return fmt.Errorf("original message missing from request context")
		}
		want, err := json.Marshal(original)
		if err != nil {
			return err
		}
		got, err := json.Marshal(snapshot.Messages[len(snapshot.Messages)-1])
		if err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("original message differs from request context tail")
		}
		if stat, err := os.Stat(filepath.Join(r.Dir, id, historyFile)); err == nil {
			pending.HistoryOffset = stat.Size()
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	data, err = json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := r.writeFile(ctx, filepath.Join(r.Dir, id, pendingContextFile), data); err != nil {
		return err
	}
	return r.finishContext(ctx, id, pending)
}

func (r *FsSessionRepo) recoverContext(ctx context.Context, id string) error {
	data, err := os.ReadFile(filepath.Join(r.Dir, id, pendingContextFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingContext
	if err := json.Unmarshal(data, &pending); err != nil {
		return fmt.Errorf("read pending context: %w", err)
	}
	if err := pending.Snapshot.Validate(); err != nil {
		return err
	}
	return r.finishContext(ctx, id, pending)
}

func (r *FsSessionRepo) finishContext(ctx context.Context, id string, pending pendingContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pending.Original != nil {
		if err := r.completeHistoryAppend(id, pending); err != nil {
			return err
		}
	}
	data, err := json.Marshal(pending.Snapshot)
	if err != nil {
		return err
	}
	if err := r.writeFile(ctx, filepath.Join(r.Dir, id, requestContextFile), data); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(r.Dir, id, pendingContextFile)); err != nil {
		return err
	}
	return syncDir(filepath.Join(r.Dir, id))
}

func (r *FsSessionRepo) completeHistoryAppend(id string, pending pendingContext) error {
	line, err := json.Marshal(pending.Original)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(filepath.Join(r.Dir, id, historyFile), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	written := stat.Size() - pending.HistoryOffset
	if pending.HistoryOffset < 0 || written < 0 || written > int64(len(line)) {
		return fmt.Errorf("history changed during pending context commit")
	}
	actual := make([]byte, int(written))
	if _, err := f.ReadAt(actual, pending.HistoryOffset); err != nil && err != io.EOF {
		return err
	}
	if !bytes.Equal(actual, line[:written]) {
		return fmt.Errorf("pending history bytes do not match")
	}
	if written < int64(len(line)) {
		if _, err := f.Write(line[written:]); err != nil {
			return err
		}
	}
	return f.Sync()
}

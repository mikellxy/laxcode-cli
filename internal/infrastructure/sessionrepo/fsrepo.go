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

// sysMessageFile 独立存放系统提示词：它每轮启动都会被整体覆盖（技能索引 /
// Plan Mode 变化），而 history.jsonl 是只追加的对话流水，两者写入语义不同。
const sysMessageFile = "sys_message.json"

// tmpPattern 是原子写（写临时文件 + rename）的临时文件名模板。
const tmpPattern = "*.tmp"

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
	// 写失败必须上报：静默丢弃会让内存里的会话与磁盘历史分叉，
	// 续聊时表现为“上一轮对话凭空消失”。
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
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

// writeFile 以“临时文件 + rename”原子替换 path，避免写一半被读到。
func (r *FsSessionRepo) writeFile(ctx context.Context, path string, data []byte) error {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
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
		return err
	}

	return nil
}

// GetMessages 读回完整消息序列：系统提示词（sys_message.json）居首，
// 其后是 history.jsonl 的对话流水。会话不存在时返回空序列而非错误。
//
// 旧版布局把系统提示词写在 history.jsonl 首行，新版改存独立文件。两者同时
// 存在时以 sys_message.json 为准，跳过 history 里的 system 行：否则续聊会
// 向模型发送两条系统提示词（新的一条 + 陈旧的一条）。
func (r *FsSessionRepo) GetMessages(ctx context.Context, sessionID string) ([]sharedkernel.Message, error) {
	var msgs []sharedkernel.Message
	hasSysFile := false
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
			hasSysFile = true
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
		if hasSysFile && msg.Role == sharedkernel.RoleSystem {
			continue // 旧布局遗留的系统提示词，已被 sys_message.json 取代
		}
		msgs = append(msgs, msg)
	}
	// 扫描中途失败（如单行超出缓冲上限）必须上报：静默截断会让续聊
	// 丢掉后半段历史，而调用方无从知晓。
	if err := scanner.Err(); err != nil {
		return nil, err
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

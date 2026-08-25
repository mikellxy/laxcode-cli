package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/schema"
)

// historyFile 是 session 对话历史的持久化文件名，位于
// <workDir>/.laxcode/.session/<sessionID>/ 下，每行一条 schema.Message 的 JSON。
// system prompt 属派生数据（由代码版本与技能集合计算而来），不写入该文件，
// 每次启动重建，续聊时模板/技能变更即时生效。
const historyFile = "history.jsonl"

// metaFile 是 session token 统计的快照文件名，与 history.jsonl 同目录。
// 重写式更新（tmp + rename），真值以 history.jsonl 重放为准。
const metaFile = "meta.json"

// Session 是对话历史的唯一真相源：内存消息列表与 history.jsonl 同步维护，
// 每 Append 一条即落盘一行。messages 非导出，强制所有追加走 Append，
// 避免内存与磁盘失同步（View 是只读组装，不改变内部状态）。
type Session struct {
	id          string
	Messages    []schema.Message
	historyPath string
	metaPath    string
	// TokenUsed 是会话累计 token 消耗，恒等于全部消息 TokenUsed 的加和
	// （仅 assistant 消息携带非零值）。raw 计费口径：每次调用的完整 input
	// （含 system prompt 与当时全部历史）都会重复计入，语义为真实账单累计。
	// 只经 Append 累加，不存在绕过消息历史的更新路径，因此可从
	// history.jsonl 单遍重放推导。
	TokenUsed schema.TokenStatistics
	// WindowToken 是上下文窗口占用快照，原样拷贝自最后一条携带非零用量的
	// assistant 消息的 TokenUsed：TokenInput+TokenOutput 之和近似
	// "system prompt + 全部历史"的当前窗口占用。其后追加 user/tool 消息到
	// 下次调用之间，真实占用大于此值（新消息 token 数本地不可知），
	// 属该口径固有滞后。旧会话恢复后为零值（未知态），下次调用刷新。
	WindowToken schema.TokenStatistics
}

// SessionMeta 是 meta.json 的文件格式：token 统计的人类可读快照，
// 供用户直接查看。统计真值始终从 history.jsonl 重放推导，本文件
// 缺失/损坏/滞后均不影响正确性。顶层对象 + version 保留扩展性，
// 未来可挂 created_at、model 等不可推导的会话元数据。
type SessionMeta struct {
	Version     int                    `json:"version"`
	TokenUsed   schema.TokenStatistics `json:"token_used"`
	WindowToken schema.TokenStatistics `json:"window_token"`
}

// newSession 以 sessionID 新建空 session；不创建任何目录或文件，
// 从未 Append 的空会话不会在磁盘留下痕迹。
func newSession(workDir, sessionID string) *Session {
	return &Session{
		id:          sessionID,
		historyPath: filepath.Join(workDir, ".laxcode", ".session", sessionID, historyFile),
		metaPath:    filepath.Join(workDir, ".laxcode", ".session", sessionID, metaFile),
	}
}

// loadSession 从磁盘恢复 session 历史：逐行读取 history.jsonl 并反序列化。
// 文件不存在视为空历史（新建会话的正常路径，静默处理）；坏行与空白行
// 跳过并警告，绝不阻断启动（与 skill 加载的容错哲学一致）。
func loadSession(workDir, sessionID string) *Session {
	s := newSession(workDir, sessionID)

	f, err := os.Open(s.historyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			warnHistoryRead(s.historyPath, err)
		}
		return s
	}
	defer f.Close()

	// 工具结果（如 read 大文件）可能远超 scanner 默认的 64KB 行上限
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue // 空白行静默跳过
		}
		var msg schema.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			warnHistoryBadLine(s.historyPath, lineNo, err)
			continue
		}
		s.Messages = append(s.Messages, msg)
		// 单遍重放恢复 token 统计：全量求和恢复累计消耗，
		// 记住最后一条非零用量消息恢复窗口占用。meta.json 不参与
		// 恢复--重放是权威，快照缺失/损坏/滞后一律以重放为准。
		s.TokenUsed.TokenInput += msg.TokenUsed.TokenInput
		s.TokenUsed.TokenOutput += msg.TokenUsed.TokenOutput
		if msg.TokenUsed != (schema.TokenStatistics{}) {
			s.WindowToken = msg.TokenUsed
		}
	}
	if err := scanner.Err(); err != nil {
		warnHistoryRead(s.historyPath, err)
	}
	return s
}

// Append 把一条消息追加进内存历史并以 O_APPEND 向 history.jsonl 追加一行 JSON，
// 首次写入前按需创建会话目录。写盘失败输出显眼警告但不中断会话：
// 内存历史仍然保留（仅磁盘缺一行），后续消息继续尝试落盘。
// token 统计在此单点维护：TokenUsed 累加保证加和不变式；
// 携带非零用量的消息（assistant）刷新 WindowToken 并重写 meta.json 快照。
func (s *Session) Append(msg schema.Message) {
	s.Messages = append(s.Messages, msg)
	s.TokenUsed.TokenInput += msg.TokenUsed.TokenInput
	s.TokenUsed.TokenOutput += msg.TokenUsed.TokenOutput

	// 先写 history 行、后重写 meta：崩溃落在中间时 history 多一行而
	// meta 滞后一条，下次加载重放自愈；反向超前不会发生。
	line, err := json.Marshal(msg)
	if err != nil {
		// schema.Message 只含可序列化字段，marshal 失败仅剩理论可能
		warnHistoryWrite(s.historyPath, err)
		return
	}
	if err := s.appendLine(line); err != nil {
		warnHistoryWrite(s.historyPath, err)
	}

	if msg.TokenUsed != (schema.TokenStatistics{}) {
		s.WindowToken = msg.TokenUsed
		s.writeMeta()
	}
}

// writeMeta 以 tmp + rename 原子重写 meta.json 快照。失败仅警告不中断会话：
// 快照本就是重放可推导的冗余数据，坏一个快照不影响正确性。
func (s *Session) writeMeta() {
	meta := SessionMeta{
		Version:     1,
		TokenUsed:   s.TokenUsed,
		WindowToken: s.WindowToken,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		warnMetaWrite(s.metaPath, err)
		return
	}

	dir := filepath.Dir(s.metaPath)
	tmp, err := os.CreateTemp(dir, "meta.*.tmp")
	if err != nil {
		warnMetaWrite(s.metaPath, err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		warnMetaWrite(s.metaPath, err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		warnMetaWrite(s.metaPath, err)
		return
	}
	if err := os.Rename(tmpName, s.metaPath); err != nil {
		os.Remove(tmpName)
		warnMetaWrite(s.metaPath, err)
	}
}

func (s *Session) appendLine(line []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.historyPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.historyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// View 组装发送给大模型的历史视图：头部为本次启动重建的 system prompt，
// 其后为 session 当前历史。每次返回新拼切片且不修改内部状态——
// 视图是廉价重拼，历史真相始终只有一份。
func (s *Session) View(sysPrompt string) {
	view := make([]schema.Message, 0, len(s.Messages)+1)
	view = append(view, schema.Message{Role: schema.RoleSystem, Content: sysPrompt})
	s.Messages = append(view, s.Messages...)
}

// SessionDB 管理 session id 到 session 对象的映射。v1 以包级全局单例存在：
// main.go 启动时经 InitSessionDB 初始化并仅加载指定的一个 session，
// 全量扫描/懒加载留待未来演进（演进时只改 InitSessionDB 内部，不动调用方）。
type SessionDB struct {
	sessions map[string]*Session
}

// sessionDB 是全局会话库：仅经 InitSessionDB 写入、getSession 读取，
// 不对外导出（参照 env.WorkDir 的包级全局先例，但收口访问路径）。
var sessionDB *SessionDB

// InitSessionDB 初始化全局会话库并仅加载 sessionID 对应的一个 session：
// 其 history.jsonl 存在则恢复历史（续聊），否则以该 id 新建空 session。
// 本版不扫描、不加载其他任何 session。
func InitSessionDB(workDir, sessionID string) {
	db := &SessionDB{sessions: make(map[string]*Session)}
	db.sessions[sessionID] = loadSession(workDir, sessionID)
	sessionDB = db
}

// getSession 按 id 从全局会话库查询 session，供 Loop 使用；
// 库未初始化或 id 不存在时返回 nil，由调用方决定如何处置。
func getSession(sessionID string) *Session {
	if sessionDB == nil {
		return nil
	}
	return sessionDB.sessions[sessionID]
}

// warnHistoryBadLine 输出跳过坏行的警告，沿用项目控制台惯例（黄色 [LaxCode] 前缀）。
func warnHistoryBadLine(path string, lineNo int, err error) {
	fmt.Printf("\033[33m[LaxCode] 跳过会话历史 %s 第 %d 行（无法反序列化为消息）: %v\033[0m\n",
		path, lineNo, err)
}

// warnHistoryRead 输出历史读取异常警告；不阻断启动，历史可能不完整。
func warnHistoryRead(path string, err error) {
	fmt.Printf("\033[33m[LaxCode] 读取会话历史 %s 异常，历史可能不完整: %v\033[0m\n", path, err)
}

// warnHistoryWrite 输出历史写盘失败警告：数据可能未保存，须比普通警告更显眼
// （红色 + WARN），但不中断会话——交互现场优先于磁盘故障。
func warnHistoryWrite(path string, err error) {
	fmt.Printf("\033[31m[LaxCode][WARN] 会话历史写入 %s 失败，本条对话可能未被保存: %v\033[0m\n",
		path, err)
}

// warnMetaWrite 输出 meta.json 写入失败警告：快照损坏不影响统计正确性
// （真值由 history.jsonl 重放推导），故比历史写失败的警告轻（黄色）。
func warnMetaWrite(path string, err error) {
	fmt.Printf("\033[33m[LaxCode] 会话统计快照写入 %s 失败: %v\033[0m\n", path, err)
}

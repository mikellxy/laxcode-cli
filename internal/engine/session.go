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

// Session 是对话历史的唯一真相源：内存消息列表与 history.jsonl 同步维护，
// 每 Append 一条即落盘一行。messages 非导出，强制所有追加走 Append，
// 避免内存与磁盘失同步（View 是只读组装，不改变内部状态）。
type Session struct {
	id          string
	messages    []schema.Message
	historyPath string
}

// newSession 以 sessionID 新建空 session；不创建任何目录或文件，
// 从未 Append 的空会话不会在磁盘留下痕迹。
func newSession(workDir, sessionID string) *Session {
	return &Session{
		id:          sessionID,
		historyPath: filepath.Join(workDir, ".laxcode", ".session", sessionID, historyFile),
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
		s.messages = append(s.messages, msg)
	}
	if err := scanner.Err(); err != nil {
		warnHistoryRead(s.historyPath, err)
	}
	return s
}

// Append 把一条消息追加进内存历史并以 O_APPEND 向 history.jsonl 追加一行 JSON，
// 首次写入前按需创建会话目录。写盘失败输出显眼警告但不中断会话：
// 内存历史仍然保留（仅磁盘缺一行），后续消息继续尝试落盘。
func (s *Session) Append(msg schema.Message) {
	s.messages = append(s.messages, msg)

	line, err := json.Marshal(msg)
	if err != nil {
		// schema.Message 只含可序列化字段，marshal 失败仅剩理论可能
		warnHistoryWrite(s.historyPath, err)
		return
	}
	if err := s.appendLine(line); err != nil {
		warnHistoryWrite(s.historyPath, err)
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
func (s *Session) View(sysPrompt string) []schema.Message {
	view := make([]schema.Message, 0, len(s.messages)+1)
	view = append(view, schema.Message{Role: schema.RoleSystem, Content: sysPrompt})
	return append(view, s.messages...)
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

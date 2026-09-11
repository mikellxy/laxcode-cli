package reactservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
	_ "modernc.org/sqlite" // 注册 database/sql 驱动名 "sqlite"，测试直查数据库
)

// integrationWorkDir 是集成测试的固定工作目录：运行前清空 .laxcode（含
// SQLite 数据库与 JSONL 冷备），运行后保留现场便于人工检查落库数据。
const integrationWorkDir = "/tmp/laxcode_test"

const (
	integrationAlpha = "alpha.txt"
	integrationBeta  = "beta.txt"
)

// countingLLM 按调用次数奇偶分支：奇数次返回带两个 read_file tool calls 的
// assistant 消息（驱动真实文件读取），偶数次返回无工具调用的最终回答。
// 消息 usage 以 sharedkernel.EstimateTokenInt 估算填充，并在 totalUsage 中
// 累计，供内存聚合与 SQLite 两侧对账。
type countingLLM struct {
	calls      int
	lastMsgs   []sharedkernel.Message
	totalUsage sharedkernel.TokenStatistics
}

func (c *countingLLM) Generate(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	c.lastMsgs = msgs
	c.calls++
	msg := &sharedkernel.Message{Role: sharedkernel.RoleAssistant}
	if c.calls%2 == 1 {
		turn := (c.calls + 1) / 2
		msg.Content = fmt.Sprintf("第 %d 轮：先读取工作目录下的两个文件", turn)
		msg.ToolCalls = []sharedkernel.ToolCall{
			{
				ID:        fmt.Sprintf("read-alpha-%d", turn),
				Name:      "read_file",
				Arguments: []byte(fmt.Sprintf(`{"path":%q,"start_line_no":1,"start_bytes":1}`, integrationAlpha)),
			},
			{
				ID:        fmt.Sprintf("read-beta-%d", turn),
				Name:      "read_file",
				Arguments: []byte(fmt.Sprintf(`{"path":%q,"start_line_no":1,"start_bytes":1}`, integrationBeta)),
			},
		}
	} else {
		msg.Content = fmt.Sprintf("第 %d 轮：两个文件均已读取，这是最终回答 %s", c.calls/2, uuid.New().String())
	}
	msg.TokenUsed = sharedkernel.TokenStatistics{
		TokenInput:  estimateMsgsTokens(c.lastMsgs),
		TokenOutput: estimateMsgTokens(*msg),
	}
	c.totalUsage.Add(msg.TokenUsed)
	return msg, nil
}

func (c *countingLLM) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	msg, err := c.Generate(ctx, msgs, defs)
	if err != nil {
		return nil, err
	}
	if msg.Content != "" {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextStart})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta, Delta: msg.Content})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextEnd})
	}
	for i := range msg.ToolCalls {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkToolCall, ToolCall: &msg.ToolCalls[i]})
	}
	return msg, nil
}

func (c *countingLLM) CountInputTokens(_ context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition) (int, error) {
	count := estimateMsgsTokens(msgs)
	for _, def := range defs {
		data, _ := json.Marshal(def)
		count += sharedkernel.EstimateTokenInt(string(data))
	}
	return count, nil
}

func (c *countingLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 200_000, ReservedOutputTokens: 20_000}
}

func estimateMsgsTokens(msgs []sharedkernel.Message) int {
	count := 0
	for _, msg := range msgs {
		count += estimateMsgTokens(msg)
	}
	return count
}

func estimateMsgTokens(msg sharedkernel.Message) int {
	count := sharedkernel.EstimateTokenInt(msg.Content)
	count += sharedkernel.EstimateTokenInt(msg.ReasoningContent)
	for _, call := range msg.ToolCalls {
		count += sharedkernel.EstimateTokenInt(call.Name)
		count += sharedkernel.EstimateTokenInt(string(call.Arguments))
	}
	return count
}

// TestChatAlignsMemorySessionWithSQLite 横跨三层验证一次完整会话：
// 真实 SQLite 仓储 + 真实 read_file 工具 + mock LLM 驱动两轮 Chat，
// 断言内存聚合的关键字段（Revision / LastSeq / MemoryGeneration / token
// 账目 / 消息序列）与数据库读回的快照完全对齐。
func TestChatAlignsMemorySessionWithSQLite(t *testing.T) {
	ctx := context.Background()
	workDir := integrationWorkDir

	// 清掉上次运行留下的 SQLite（含 WAL/SHM）与 JSONL 冷备；uuid session id
	// 保证即使残留行存在也不会影响本次。
	if err := os.RemoveAll(filepath.Join(workDir, layout.RootDirName)); err != nil {
		t.Fatalf("清理历史 sqlite: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建工作目录: %v", err)
	}
	alphaContent := "alpha line 1\nalpha line 2\nalpha line 3\n"
	betaContent := "beta line 1\nbeta line 2\n"
	for name, content := range map[string]string{integrationAlpha: alphaContent, integrationBeta: betaContent} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入 %s: %v", name, err)
		}
	}

	sessionID := uuid.New().String()
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(workDir), layout.SessionRoot(workDir))
	if err != nil {
		t.Fatalf("打开 SQLite 仓储: %v", err)
	}

	llm := &countingLLM{}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewReadFileTool(workDir, workfs.New()))
	svc := NewReActService(session.NewSession(sessionID), repo, llm, nil, reg, func(*ReactEvent) {}, nil)
	if err := svc.InitSession(ctx); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if err := svc.InitSysPrompt(ctx, "集成测试系统提示词"); err != nil {
		t.Fatalf("InitSysPrompt: %v", err)
	}

	// 随机用户问题连问两轮：每轮 = 一次带工具调用的生成 + 两次真实读文件 + 一次收束。
	questions := []string{
		"随机问题一 " + uuid.New().String(),
		"随机问题二 " + uuid.New().String(),
	}
	for i, question := range questions {
		final, err := svc.Chat(ctx, question)
		if err != nil {
			t.Fatalf("Chat #%d: %v", i+1, err)
		}
		if final == nil || len(final.ToolCalls) != 0 {
			t.Fatalf("Chat #%d 应返回无工具调用的最终消息，实际 %+v", i+1, final)
		}
	}

	// ===== 内存聚合断言 =====
	sess := svc.Session
	if llm.calls != 4 {
		t.Errorf("两轮 Chat 应各触发 2 次生成（工具轮+收束轮），实际 %d", llm.calls)
	}
	// system + 2×(user + assistant(tool calls) + tool + tool + assistant) = 11 条，
	// 每次原子提交推进 Revision 与 LastSeq 各 1。
	if sess.Revision != 11 {
		t.Errorf("内存 Revision 应为 11，实际 %d", sess.Revision)
	}
	if sess.LastSeq != 11 {
		t.Errorf("内存 LastSeq 应为 11，实际 %d", sess.LastSeq)
	}
	if sess.MemoryGeneration != 1 {
		t.Errorf("未触发压缩，MemoryGeneration 应保持 1，实际 %d", sess.MemoryGeneration)
	}
	if len(sess.Messages) != 11 {
		t.Fatalf("内存消息应 11 条，实际 %d：%+v", len(sess.Messages), sess.Messages)
	}
	wantRoles := []string{
		sharedkernel.RoleSystem,
		sharedkernel.RoleUser, sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool, sharedkernel.RoleAssistant,
		sharedkernel.RoleUser, sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool, sharedkernel.RoleAssistant,
	}
	for i, want := range wantRoles {
		if sess.Messages[i].Role != want {
			t.Fatalf("第 %d 条消息角色应为 %s，实际 %s：%+v", i, want, sess.Messages[i].Role, sess.Messages[i])
		}
	}
	if sess.Messages[1].Content != questions[0] || sess.Messages[6].Content != questions[1] {
		t.Errorf("用户消息应与提问对齐：%q / %q", sess.Messages[1].Content, sess.Messages[6].Content)
	}
	// 工具结果确实来自真实文件读取（read_file 输出 = 内容 + 分页脚注）。
	if !strings.Contains(sess.Messages[3].Content, "alpha line 1") {
		t.Errorf("第 1 条工具结果应包含 alpha 文件内容，实际 %q", sess.Messages[3].Content)
	}
	if !strings.Contains(sess.Messages[4].Content, "beta line 1") {
		t.Errorf("第 2 条工具结果应包含 beta 文件内容，实际 %q", sess.Messages[4].Content)
	}
	if sess.Messages[3].ToolCallID != "read-alpha-1" || sess.Messages[4].ToolCallID != "read-beta-1" {
		t.Errorf("工具结果应回填对应 ToolCallID，实际 %q / %q",
			sess.Messages[3].ToolCallID, sess.Messages[4].ToolCallID)
	}
	if sess.TokenUsed != llm.totalUsage {
		t.Errorf("内存累计用量应等于 4 次 assistant usage 之和：内存 %+v，mock 累计 %+v",
			sess.TokenUsed, llm.totalUsage)
	}

	// ===== SQLite 对齐断言：关闭写侧连接，从数据库重新读回 =====
	if err := repo.Close(); err != nil {
		t.Fatalf("关闭写侧仓储: %v", err)
	}
	readRepo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(workDir), layout.SessionRoot(workDir))
	if err != nil {
		t.Fatalf("重开 SQLite 仓储: %v", err)
	}
	t.Cleanup(func() { _ = readRepo.Close() })
	loaded, err := readRepo.GetRequestContext(ctx, sessionID)
	if err != nil {
		t.Fatalf("从 SQLite 读回会话: %v", err)
	}
	if loaded.Revision != sess.Revision {
		t.Errorf("SQLite Revision=%d 与内存 %d 不对齐", loaded.Revision, sess.Revision)
	}
	if loaded.LastSeq != sess.LastSeq {
		t.Errorf("SQLite LastSeq=%d 与内存 %d 不对齐", loaded.LastSeq, sess.LastSeq)
	}
	if loaded.MemoryGeneration != sess.MemoryGeneration {
		t.Errorf("SQLite MemoryGeneration=%d 与内存 %d 不对齐", loaded.MemoryGeneration, sess.MemoryGeneration)
	}
	if loaded.TokenUsed != sess.TokenUsed {
		t.Errorf("SQLite TokenUsed=%+v 与内存 %+v 不对齐", loaded.TokenUsed, sess.TokenUsed)
	}
	if loaded.WindowToken != sess.WindowToken {
		t.Errorf("SQLite WindowToken=%+v 与内存 %+v 不对齐", loaded.WindowToken, sess.WindowToken)
	}
	if !reflect.DeepEqual(loaded.Messages, sess.Messages) {
		for i := range sess.Messages {
			if i >= len(loaded.Messages) || !reflect.DeepEqual(loaded.Messages[i], sess.Messages[i]) {
				t.Errorf("第 %d 条消息落库后不一致：sqlite %+v，内存 %+v",
					i, loaded.Messages[i], sess.Messages[i])
			}
		}
		t.Fatalf("SQLite 消息序列与内存不对齐：sqlite %d 条，内存 %d 条", len(loaded.Messages), len(sess.Messages))
	}
}

// compactionLLM 在指定调用序号（finalOnCalls）返回无工具调用的最终回答，
// 其余调用一律返回两个 read_file tool calls，用于在一个 Chat 内叠出多个
// tool-call 组。usage 经 EstimateTokenInt 估算并逐次记录。ContextBudget 与
// 真实 OpenApiProvider 同源：直接取 config.EnvAndFileConf 的窗口配置。
type compactionLLM struct {
	finalOnCalls map[int]bool
	calls        int
	lastMsgs     []sharedkernel.Message
	usages       []sharedkernel.TokenStatistics
	totalUsage   sharedkernel.TokenStatistics
}

func (c *compactionLLM) Generate(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	c.lastMsgs = msgs
	c.calls++
	msg := &sharedkernel.Message{Role: sharedkernel.RoleAssistant}
	if c.finalOnCalls[c.calls] {
		msg.Content = fmt.Sprintf("第 %d 次生成的最终回答 %s", c.calls, uuid.New().String())
	} else {
		msg.Content = fmt.Sprintf("第 %d 次生成：读取工作目录下的两个文件", c.calls)
		msg.ToolCalls = []sharedkernel.ToolCall{
			{
				ID:        fmt.Sprintf("read-alpha-%d", c.calls),
				Name:      "read_file",
				Arguments: []byte(fmt.Sprintf(`{"path":%q,"start_line_no":1,"start_bytes":1}`, integrationAlpha)),
			},
			{
				ID:        fmt.Sprintf("read-beta-%d", c.calls),
				Name:      "read_file",
				Arguments: []byte(fmt.Sprintf(`{"path":%q,"start_line_no":1,"start_bytes":1}`, integrationBeta)),
			},
		}
	}
	msg.TokenUsed = sharedkernel.TokenStatistics{
		TokenInput:  estimateMsgsTokens(c.lastMsgs),
		TokenOutput: estimateMsgTokens(*msg),
	}
	c.usages = append(c.usages, msg.TokenUsed)
	c.totalUsage.Add(msg.TokenUsed)
	return msg, nil
}

func (c *compactionLLM) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, emit func(chunk sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	msg, err := c.Generate(ctx, msgs, defs)
	if err != nil {
		return nil, err
	}
	if msg.Content != "" {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextStart})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta, Delta: msg.Content})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextEnd})
	}
	for i := range msg.ToolCalls {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkToolCall, ToolCall: &msg.ToolCalls[i]})
	}
	return msg, nil
}

func (c *compactionLLM) CountInputTokens(_ context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition) (int, error) {
	count := estimateMsgsTokens(msgs)
	for _, def := range defs {
		data, _ := json.Marshal(def)
		count += sharedkernel.EstimateTokenInt(string(data))
	}
	return count, nil
}

func (c *compactionLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{
		ContextWindow:        config.EnvAndFileConf.OpenaiContextWindow,
		ReservedOutputTokens: config.EnvAndFileConf.OpenaiMaxOutputTokens,
	}
}

// contentWithTokenCount 构造 token 数不少于 target（且少于 target+单个
// 重复单元）的多行文本，行数控制在 read_file 单次 2000 行上限内。
func contentWithTokenCount(target int) string {
	const unit = "0123456789\n"
	lo, hi := 0, target*3
	for lo < hi {
		mid := (lo + hi) / 2
		if sharedkernel.EstimateTokenInt(strings.Repeat(unit, mid)) >= target {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return strings.Repeat(unit, lo)
}

// fetchMessageRow 直查 messages 表单行，返回正文与 artifact_id。
func fetchMessageRow(t *testing.T, db *sql.DB, ctx context.Context, sessionID, msgType string, generation, seq uint64) (string, sql.NullString) {
	t.Helper()
	var content string
	var artifact sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT content, artifact_id FROM messages
		 WHERE session_id = ? AND message_type = ? AND memory_generation = ? AND seq = ?`,
		sessionID, msgType, generation, seq).Scan(&content, &artifact)
	if err != nil {
		t.Fatalf("查 messages(type=%s, gen=%d, seq=%d): %v", msgType, generation, seq, err)
	}
	return content, artifact
}

// TestChatCompactionPersistsGenerationsInSQLite 验证上下文压缩的完整链路：
// 真实 SQLite + 真实 read_file + artifact 归档，token 计数全部走
// EstimateTokenInt 真实估算（无 countFn 强制值）。
//
// 结构说明：需求原定“第 3 次调用触发压缩”，但 compactor 的保护区规则
// （KeepRecentToolCallGroups=3，最近 3 个 tool-call 组不可裁剪）决定了
// 少于 5 个组时压缩必然无法达标（可回收节省 ≤ 前置组数×单组工具输出，
// 而 80% 触发 → 60% 目标的落差需要两个完整组才能填平），Chat 会直接
// 返回 ErrContextTargetNotReach。因此本用例：
//   - chat 1 = 2 次生成（工具轮 + 收束轮），计数低于 80% 触发阈值；
//   - chat 2 连续 4 个工具轮后，第 7 次生成（收束轮）前计数跨过阈值，
//     压缩触发并把前两组工具输出归档为 artifact，generation 推进到 2。
func TestChatCompactionPersistsGenerationsInSQLite(t *testing.T) {
	ctx := context.Background()
	workDir := integrationWorkDir

	// 窗口预算取自 config，与组合根构造 OpenApiProvider 的入参同源：
	// maxInput=15000，触发阈值 80%=12000，压缩目标 60%=9000。
	config.EnvAndFileConf.OpenaiContextWindow = 20000
	config.EnvAndFileConf.OpenaiMaxOutputTokens = 5000

	if err := os.RemoveAll(filepath.Join(workDir, layout.RootDirName)); err != nil {
		t.Fatalf("清理历史 sqlite: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建工作目录: %v", err)
	}
	// 每个工具轮读两个文件共约 2430 token：4 轮 ≈ 9700+开销 < 12000（不提前
	// 触发），5 轮 ≥ 12000（触发），压缩掉前两组后 ≈ 8200 ≤ 9000（达标）。
	alphaContent := contentWithTokenCount(1200)
	betaContent := contentWithTokenCount(1200)
	for name, content := range map[string]string{integrationAlpha: alphaContent, integrationBeta: betaContent} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入 %s: %v", name, err)
		}
	}

	sessionID := uuid.New().String()
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(workDir), layout.SessionRoot(workDir))
	if err != nil {
		t.Fatalf("打开 SQLite 仓储: %v", err)
	}
	// 压缩会把前置组的工具输出归档成 artifact，必须装配 artifact store。
	artifacts := artifactstore.New(layout.SessionRoot(workDir))

	// 第 2、7 次生成收束对话；其余调用返回工具调用。
	llm := &compactionLLM{finalOnCalls: map[int]bool{2: true, 7: true}}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewReadFileTool(workDir, workfs.New()))
	svc := NewReActService(session.NewSession(sessionID), repo, llm, nil, reg, func(*ReactEvent) {}, nil, artifacts)
	if err := svc.InitSession(ctx); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if err := svc.InitSysPrompt(ctx, "集成测试系统提示词"); err != nil {
		t.Fatalf("InitSysPrompt: %v", err)
	}

	// ===== chat 1：2 次生成，低于压缩阈值 =====
	if _, err := svc.Chat(ctx, "随机问题一 "+uuid.New().String()); err != nil {
		t.Fatalf("Chat #1: %v", err)
	}
	sess := svc.Session
	if llm.calls != 2 {
		t.Fatalf("chat 1 应恰好 2 次生成，实际 %d", llm.calls)
	}
	if sess.Revision != 6 || sess.LastSeq != 6 || sess.MemoryGeneration != 1 || len(sess.Messages) != 6 {
		t.Fatalf("chat 1 后应为 rev=6 seq=6 gen=1 共 6 条，实际 rev=%d seq=%d gen=%d 共 %d 条",
			sess.Revision, sess.LastSeq, sess.MemoryGeneration, len(sess.Messages))
	}

	// ===== chat 2：4 个工具轮后，收束生成前触发压缩 =====
	if _, err := svc.Chat(ctx, "随机问题二 "+uuid.New().String()); err != nil {
		t.Fatalf("Chat #2: %v", err)
	}
	if llm.calls != 7 {
		t.Fatalf("chat 2 应新增 5 次生成（4 工具轮+收束轮），实际总调用 %d", llm.calls)
	}

	// ===== 内存聚合断言 =====
	sess = svc.Session
	// 20 条消息各推进 1 次 revision（sys 1 + 19 条），压缩提交再 +1。
	if sess.Revision != 21 {
		t.Errorf("内存 Revision 应为 21，实际 %d", sess.Revision)
	}
	if sess.LastSeq != 20 {
		t.Errorf("内存 LastSeq 应为 20，实际 %d", sess.LastSeq)
	}
	if sess.MemoryGeneration != 2 {
		t.Errorf("压缩成功后 MemoryGeneration 应为 2，实际 %d", sess.MemoryGeneration)
	}
	if len(sess.Messages) != 20 {
		t.Fatalf("内存消息应 20 条，实际 %d", len(sess.Messages))
	}
	wantRoles := []string{
		sharedkernel.RoleSystem,
		sharedkernel.RoleUser, sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool,
		sharedkernel.RoleAssistant,
		sharedkernel.RoleUser,
		sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool,
		sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool,
		sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool,
		sharedkernel.RoleAssistant, sharedkernel.RoleTool, sharedkernel.RoleTool,
		sharedkernel.RoleAssistant,
	}
	for i, want := range wantRoles {
		if sess.Messages[i].Role != want {
			t.Fatalf("第 %d 条消息角色应为 %s，实际 %s", i+1, want, sess.Messages[i].Role)
		}
	}
	// 前两组（seq 4/5/9/10）工具输出被替换为归档占位符并带 artifact 引用；
	// 保护区内的最近三组（seq 12 起）保持原文。
	archived := 0
	for i, msg := range sess.Messages {
		if msg.Role != sharedkernel.RoleTool {
			continue
		}
		if i+1 <= 10 { // seq 4,5,9,10
			if msg.Artifact == nil || !strings.Contains(msg.Content, "已归档") {
				t.Errorf("seq %d 工具输出应为归档占位符并带 artifact，实际 %+v", i+1, msg)
			}
			archived++
		} else if msg.Artifact != nil || !strings.Contains(msg.Content, "0123456789") {
			t.Errorf("seq %d 工具输出应保持原文，实际 %q", i+1, msg.Content[:40])
		}
	}
	if archived != 4 {
		t.Errorf("应归档 4 条工具输出（2 组×2），实际 %d", archived)
	}
	if sess.TokenUsed != llm.totalUsage {
		t.Errorf("内存累计用量应等于 7 次 assistant usage 之和：内存 %+v，mock 累计 %+v",
			sess.TokenUsed, llm.totalUsage)
	}
	if sess.WindowToken != llm.usages[6] {
		t.Errorf("窗口占用应被最后一条 assistant 实测值覆盖：内存 %+v，第 7 次 usage %+v",
			sess.WindowToken, llm.usages[6])
	}

	// ===== SQLite 对齐断言 =====
	if err := repo.Close(); err != nil {
		t.Fatalf("关闭写侧仓储: %v", err)
	}
	readRepo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(workDir), layout.SessionRoot(workDir))
	if err != nil {
		t.Fatalf("重开 SQLite 仓储: %v", err)
	}
	t.Cleanup(func() { _ = readRepo.Close() })

	// request_contexts 头部三字段与内存对齐。
	var dbRevision, dbGeneration, dbLastSeq uint64
	rawDB, err := sql.Open("sqlite", layout.SessionDB(workDir))
	if err != nil {
		t.Fatalf("打开原始连接: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	if err := rawDB.QueryRowContext(ctx,
		`SELECT revision, memory_generation, last_seq FROM request_contexts WHERE session_id = ?`,
		sessionID).Scan(&dbRevision, &dbGeneration, &dbLastSeq); err != nil {
		t.Fatalf("查 request_contexts: %v", err)
	}
	if dbRevision != sess.Revision || dbGeneration != sess.MemoryGeneration || dbLastSeq != sess.LastSeq {
		t.Errorf("SQLite 头部与内存不对齐：db(rev=%d gen=%d seq=%d) vs 内存(rev=%d gen=%d seq=%d)",
			dbRevision, dbGeneration, dbLastSeq, sess.Revision, sess.MemoryGeneration, sess.LastSeq)
	}

	// messages 表应有 1 份 original + 两个 generation 的 in_memory。
	type groupRow struct {
		count, minSeq, maxSeq uint64
	}
	rows, err := rawDB.QueryContext(ctx,
		`SELECT message_type, memory_generation, COUNT(*), MIN(seq), MAX(seq)
		 FROM messages WHERE session_id = ?
		 GROUP BY message_type, memory_generation`, sessionID)
	if err != nil {
		t.Fatalf("查 messages 分组: %v", err)
	}
	got := make(map[string]map[uint64]groupRow)
	for rows.Next() {
		var msgType string
		var gen, count, minSeq, maxSeq uint64
		if err := rows.Scan(&msgType, &gen, &count, &minSeq, &maxSeq); err != nil {
			t.Fatalf("扫描 messages 分组: %v", err)
		}
		if got[msgType] == nil {
			got[msgType] = make(map[uint64]groupRow)
		}
		got[msgType][gen] = groupRow{count, minSeq, maxSeq}
	}
	rows.Close()
	wantGroups := map[string]map[uint64]groupRow{
		// original 恒为 memory_generation=0：20 条不可变全文，seq 1..20。
		"original": {0: {20, 1, 20}},
		// gen1：压缩前的工作集封存，19 条（不含压缩后追加的收束消息）。
		"in_memory": {
			1: {19, 1, 19},
			2: {20, 1, 20}, // gen2：当前工作集，含归档占位符，20 条。
		},
	}
	if !reflect.DeepEqual(got, wantGroups) {
		t.Errorf("messages 分组不符：got %+v，want %+v", got, wantGroups)
	}

	// 同一条消息（seq=4，第一组 alpha 结果）在三种存储形态下：
	// original 全文且无 artifact；gen1 全文；gen2 占位符 + artifact_id。
	origContent, origArtifact := fetchMessageRow(t, rawDB, ctx, sessionID, "original", 0, 4)
	if !strings.Contains(origContent, "0123456789") || origArtifact.Valid {
		t.Errorf("original seq=4 应为全文且无 artifact：artifact=%v content=%.40s", origArtifact, origContent)
	}
	gen1Content, gen1Artifact := fetchMessageRow(t, rawDB, ctx, sessionID, "in_memory", 1, 4)
	if !strings.Contains(gen1Content, "0123456789") || gen1Artifact.Valid {
		t.Errorf("gen1 in_memory seq=4 应为全文且无 artifact：artifact=%v", gen1Artifact)
	}
	gen2Content, gen2Artifact := fetchMessageRow(t, rawDB, ctx, sessionID, "in_memory", 2, 4)
	if !strings.Contains(gen2Content, "已归档") || !gen2Artifact.Valid {
		t.Errorf("gen2 in_memory seq=4 应为占位符且带 artifact_id：artifact=%v", gen2Artifact)
	}

	// GetRequestContext 只读最新 generation：内容与内存工作集完全对齐。
	loaded, err := readRepo.GetRequestContext(ctx, sessionID)
	if err != nil {
		t.Fatalf("从 SQLite 读回会话: %v", err)
	}
	if loaded.MemoryGeneration != 2 {
		t.Errorf("读回应为最新 generation=2，实际 %d", loaded.MemoryGeneration)
	}
	if !reflect.DeepEqual(loaded, sess.Snapshot()) {
		t.Errorf("SQLite 最新工作集与内存快照不对齐：\nsqlite %+v\n内存 %+v", loaded, sess.Snapshot())
	}

	// ===== 补充校验：artifact 可读回原文，JSONL 冷备保序 =====
	if len(sess.Messages[3].Artifact.ID) == 0 {
		t.Fatalf("内存 seq=4 应带 artifact 引用")
	}
	page, err := artifacts.ReadArtifact(ctx, sessionID, sess.Messages[3].Artifact.ID, 0, tools.MaxArtifactPageRunes)
	if err != nil {
		t.Fatalf("读回 artifact: %v", err)
	}
	if page.Content != origContent || !page.EOF {
		t.Errorf("artifact 内容应等于 original 原文：len(artifact)=%d len(original)=%d", len(page.Content), len(origContent))
	}
	rawHistory, err := os.ReadFile(filepath.Join(layout.SessionRoot(workDir), sessionID, "history.jsonl"))
	if err != nil {
		t.Fatalf("读 history.jsonl: %v", err)
	}
	historyLines := strings.Split(strings.TrimSpace(string(rawHistory)), "\n")
	if len(historyLines) != 20 {
		t.Errorf("JSONL 冷备应 20 行（每次原子提交一行 original），实际 %d", len(historyLines))
	}
	if !strings.Contains(historyLines[3], "0123456789") {
		t.Errorf("JSONL 第 4 行应保留 alpha 原文，实际 %s", historyLines[3][:40])
	}
}

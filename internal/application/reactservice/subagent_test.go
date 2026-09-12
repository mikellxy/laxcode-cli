package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/shell"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

// newTestSubAgent 构造子 Agent 并按组合根的真实装配方式注入端口：子工具集
// 虽在本组用例中不会被执行，仍注入真实现，避免 nil 端口掩盖装配缺陷。
func newTestSubAgent(parent *ReActService, workDir string) *SubAgent {
	return NewSubAgent(parent, workDir, SubAgentDeps{
		WorkFS:   workfs.New(),
		NewShell: func() tools.ShellRunner { return shell.New() },
		SkillSrc: skillrepo.New(),
	})
}

// childSysPrompt 从 repo 中取出子会话（ID 以 sub: 前缀）的 system 消息内容：
// 系统提示词属于当前工作集，不进入不可变原始历史。
func childSysPrompt(t *testing.T, repo *memRepo) string {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for id, sys := range repo.sysMsgs {
		if strings.HasPrefix(id, "sub:") {
			return sys.Content
		}
	}
	t.Fatalf("repo 中未找到子会话的 system 消息，实际会话桶: %v", sessionIDs(repo.sysMsgs))
	return ""
}

// sessionIDs 抽出 repo 中已有的会话 ID，仅用于失败时的诊断输出。
func sessionIDs[K any](m map[string]K) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}

func TestSubAgentNameAndDefinition(t *testing.T) {
	parent := &ReActService{}
	sa := newTestSubAgent(parent, "/tmp/wd")
	if sa.Name() != tools.ToolRunSubAgent {
		t.Errorf("Name 应为 run_sub_agent，实际 %q", sa.Name())
	}

	def := sa.Definition()
	if def.Name != tools.ToolRunSubAgent {
		t.Errorf("Definition.Name 不符：%q", def.Name)
	}
	params, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Definition.Parameters 应含 properties：%v", def.Parameters)
	}
	if _, ok := params["task"]; !ok {
		t.Errorf("task 属性缺失：%v", params)
	}
	if _, ok := params["abstract"]; !ok {
		t.Errorf("abstract 属性缺失：%v", params)
	}
	required, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required 应为字符串数组：%v", def.Parameters["required"])
	}
	joined := strings.Join(required, ",")
	if !strings.Contains(joined, "task") || !strings.Contains(joined, "abstract") {
		t.Errorf("required 应含 task 与 abstract：%v", required)
	}
}

func TestSubAgentExecuteBadJSON(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	_, err := sa.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err == nil || !strings.Contains(err.Error(), "parsing sub-agent args") {
		t.Fatalf("非法 JSON 应返回解析错误，实际 %v", err)
	}
}

func TestSubAgentExecuteMissingTask(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	_, err := sa.Execute(context.Background(), json.RawMessage(`{"abstract":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "sub-agent task required") {
		t.Fatalf("缺 task 应报错，实际 %v", err)
	}
}

func TestSubAgentExecuteHappyPath(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsg("child result"), finishReason: sharedkernel.FinishReasonStop},
	}}
	parent := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"count files","abstract":"counting","work_dir":"/tmp/wd"}`))
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("工具结果应为合法 SubAgentResult JSON：%v\n%s", err, out)
	}
	if res.Status != SubAgentStatusComplete {
		t.Errorf("stop 结束应标记 complete，实际 %q", res.Status)
	}
	if res.Report != "child result" {
		t.Errorf("report 应为子 Agent 结论文本，实际 %q", res.Report)
	}
	if res.ChildSessionID == "" || !strings.HasPrefix(res.ChildSessionID, "sub:") {
		t.Errorf("child_session_id 应为 sub: 前缀，实际 %q", res.ChildSessionID)
	}
	if res.FinishReason != sharedkernel.FinishReasonStop {
		t.Errorf("finish_reason 应为 stop，实际 %q", res.FinishReason)
	}
	if res.Usage.Turns != 1 {
		t.Errorf("usage.turns 应为 1，实际 %d", res.Usage.Turns)
	}
	// 父会话不受影响（子会话独立、绝不写回）
	if len(sess.Messages) != 1 {
		t.Errorf("父会话不应被写入，实际 %d 条：%+v", len(sess.Messages), sess.Messages)
	}
	// 子会话落在独立 ID（sub: 前缀）下，repo 中可读到：
	// 系统提示词单独一份，对话流水里是 user + assistant 两条
	total := 0
	roles := map[string]int{}
	repo.mu.Lock()
	for id, msgs := range repo.msgs {
		if strings.HasPrefix(id, "sub:") {
			total += len(msgs)
			for _, m := range msgs {
				roles[m.Role]++
			}
		}
	}
	sysCnt := 0
	for id, sys := range repo.sysMsgs {
		if strings.HasPrefix(id, "sub:") {
			sysCnt++
			_ = sys
		}
	}
	repo.mu.Unlock()
	if total != 3 {
		t.Errorf("子会话 original 应为 system/user/assistant 三条，实际 %d", total)
	}
	if sysCnt != 1 {
		t.Errorf("子会话应单独存一份系统提示词，实际 %d", sysCnt)
	}
	if roles[sharedkernel.RoleSystem] != 1 || roles[sharedkernel.RoleUser] != 1 ||
		roles[sharedkernel.RoleAssistant] != 1 {
		t.Errorf("子会话消息角色分布不符：%v", roles)
	}
}

// TestSubAgentExecuteTruncatedReportIsPartial 复现评估事故的编排：子 Agent
// 最终报告顶到 max_output_tokens 截断（finish_reason=max_output_tokens），
// 结果应标记 partial 而非 complete，主 Agent 不再把半份报告当成功。
func TestSubAgentExecuteTruncatedReportIsPartial(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsg("6. **可观测"), finishReason: sharedkernel.FinishReasonMaxOutputTokens},
	}}
	parent := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t","abstract":"x"}`))
	if err != nil {
		t.Fatalf("截断不应返回 error（父循环不中断）：%v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("应返回合法 JSON：%v\n%s", err, out)
	}
	if res.Status != SubAgentStatusPartial {
		t.Errorf("截断报告应标记 partial，实际 %q", res.Status)
	}
	if res.FinishReason != sharedkernel.FinishReasonMaxOutputTokens {
		t.Errorf("finish_reason 应为 max_output_tokens，实际 %q", res.FinishReason)
	}
	if res.Report != "6. **可观测" {
		t.Errorf("半份报告应保留在 report 字段，实际 %q", res.Report)
	}
}

// TestSubAgentExecuteUsageUnavailableIsPartial 验证 provider 未修复场景的
// 兜底：流正常结束但无终止事件（usage_unavailable）也不判 complete。
func TestSubAgentExecuteUsageUnavailableIsPartial(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsg("report"), finishReason: sharedkernel.FinishReasonUsageUnavailable},
	}}
	parent := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t"}`))
	if err != nil {
		t.Fatalf("Execute：%v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("应返回合法 JSON：%v", err)
	}
	if res.Status != SubAgentStatusPartial {
		t.Errorf("usage_unavailable 应标记 partial，实际 %q", res.Status)
	}
}

func TestSubAgentExecuteChildFailureReturnsString(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{err: errors.New("child llm failed")},
	}}
	parent := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if err != nil {
		t.Fatalf("子 Agent 内部失败不应中断父循环（error 应为 nil），实际 %v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("失败也应以结构化 JSON 返回：%v\n%s", err, out)
	}
	if res.Status != SubAgentStatusFailed {
		t.Errorf("运行失败应标记 failed，实际 %q", res.Status)
	}
	if !strings.Contains(res.Report, "child llm failed") {
		t.Errorf("失败原因应在 report 字段，实际 %q", res.Report)
	}
}

// TestSubAgentExecuteCancelled 验证 ctx 取消传播时标记 cancelled 而非
// failed：父 Agent 据此区分“用户主动停止”与“子任务故障”。
func TestSubAgentExecuteCancelled(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{err: context.Canceled},
	}}
	parent := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t"}`))
	if err != nil {
		t.Fatalf("取消不应返回 error：%v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("应返回合法 JSON：%v", err)
	}
	if res.Status != SubAgentStatusCancelled {
		t.Errorf("ctx 取消应标记 cancelled，实际 %q", res.Status)
	}
}

// TestSubAgentResultJSONStableGolden 锁定结果协议的 JSON 形态：字段名与
// 顺序稳定，防止后续改动悄悄漂移（父侧消费方依赖该结构）。
func TestSubAgentResultJSONStableGolden(t *testing.T) {
	res := SubAgentResult{
		Status:         SubAgentStatusPartial,
		ChildSessionID: "sub:20260912-164713.063-parent",
		FinishReason:   sharedkernel.FinishReasonMaxOutputTokens,
		Report:         "half report",
		Usage:          Usage{InputTokens: 20327, OutputTokens: 16384, Turns: 12, ToolCalls: 18},
	}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal：%v", err)
	}
	want := `{"status":"partial","child_session_id":"sub:20260912-164713.063-parent","finish_reason":"max_output_tokens","report":"half report","usage":{"input_tokens":20327,"output_tokens":16384,"turns":12,"tool_calls":18}}`
	if string(got) != want {
		t.Errorf("SubAgentResult JSON 形态漂移：\n got: %s\nwant: %s", got, want)
	}
}

// TestClassifySubAgentRun 覆盖状态判定的纯函数分支表。
func TestClassifySubAgentRun(t *testing.T) {
	cases := []struct {
		name string
		msg  *sharedkernel.Message
		stat *RunStats
		err  error
		want string
	}{
		{"nil msg nil err", nil, nil, nil, SubAgentStatusPartial},
		{"stop via stats", nil, &RunStats{FinishReason: sharedkernel.FinishReasonStop}, nil, SubAgentStatusComplete},
		{"stop via msg", &sharedkernel.Message{FinishReason: sharedkernel.FinishReasonStop}, nil, nil, SubAgentStatusComplete},
		{"stats 覆盖 msg", &sharedkernel.Message{FinishReason: sharedkernel.FinishReasonStop}, &RunStats{FinishReason: sharedkernel.FinishReasonMaxOutputTokens}, nil, SubAgentStatusPartial},
		{"max_output_tokens", nil, &RunStats{FinishReason: sharedkernel.FinishReasonMaxOutputTokens}, nil, SubAgentStatusPartial},
		{"usage_unavailable", nil, &RunStats{FinishReason: sharedkernel.FinishReasonUsageUnavailable}, nil, SubAgentStatusPartial},
		{"空 finish_reason", nil, &RunStats{}, nil, SubAgentStatusPartial},
		{"运行错误", nil, nil, errors.New("llm boom"), SubAgentStatusFailed},
		{"ctx 取消", nil, nil, context.Canceled, SubAgentStatusCancelled},
		{"ctx 超时", nil, nil, context.DeadlineExceeded, SubAgentStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySubAgentRun(tc.msg, tc.stat, tc.err); got != tc.want {
				t.Errorf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubAgentChildPromptIncludesSkills 验证子 Agent 的系统提示词按子工作目录
// 加载技能索引：SkillSrc 无状态且 workDir 按调用传入，所以子 Agent 跑在与父
// 不同的目录时，拿到的是那个目录下的技能而非父目录的。
func TestSubAgentChildPromptIncludesSkills(t *testing.T) {
	childWorkDir := t.TempDir()
	skillDir := filepath.Join(childWorkDir, ".laxcode", "skills", "pdf-tools")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	skillMD := "---\nname: pdf-tools\ndescription: 根据文档内容生成 PDF\n---\n\n# 正文\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md 失败: %v", err)
	}

	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("ok")}},
	}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	// 构造时给一个不存在技能的目录，确保索引确实来自 work_dir 入参覆盖
	sa := newTestSubAgent(parent, t.TempDir())

	args, err := json.Marshal(map[string]string{"task": "t", "work_dir": childWorkDir})
	if err != nil {
		t.Fatalf("构造入参失败: %v", err)
	}
	if _, err := sa.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	sysPrompt := childSysPrompt(t, repo)
	if !strings.Contains(sysPrompt, "- pdf-tools: 根据文档内容生成 PDF") {
		t.Errorf("子 Agent 系统提示应含子工作目录下的技能索引，实际:\n%s", sysPrompt)
	}
	if !strings.Contains(sysPrompt, childWorkDir) {
		t.Errorf("子 Agent 系统提示应含子工作目录 %q（沙箱约束依赖它）", childWorkDir)
	}
	// plan 传 nil：子 Agent 不支持 Plan Mode，也不为会话目录编造取值
	if strings.Contains(sysPrompt, "Plan Mode") || strings.Contains(sysPrompt, "plan.md") {
		t.Errorf("子 Agent 不应包含 Plan Mode 段落，实际:\n%s", sysPrompt)
	}
}

// TestSubAgentChildPromptNoSkillsNoIndex 验证子工作目录下没有技能时，
// 系统提示整段省略技能索引（不输出空标题或占位文本）。
func TestSubAgentChildPromptNoSkillsNoIndex(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("ok")}},
	}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, t.TempDir())

	if _, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sysPrompt := childSysPrompt(t, repo); strings.Contains(sysPrompt, "## 可用技能（Skills）") {
		t.Errorf("无技能时应省略技能索引段，实际:\n%s", sysPrompt)
	}
}

func TestSubAgentBeforeAfterExecInfo(t *testing.T) {
	sa := newTestSubAgent(&ReActService{}, "/tmp/wd")

	if got := sa.BeforeExecInfo(json.RawMessage(`{}`)); got != "sub agent run to explore..." {
		t.Errorf("空 abstract 应返回占位文案，实际 %q", got)
	}
	if got := sa.BeforeExecInfo(json.RawMessage(`{"abstract":"find the bug"}`)); got != "sub agent run to explore: find the bug" {
		t.Errorf("abstract 应拼进展示文案，实际 %q", got)
	}
	// 非法 JSON 也应静默降级为占位文案
	if got := sa.BeforeExecInfo(json.RawMessage(`x`)); got != "sub agent run to explore..." {
		t.Errorf("非法 JSON 应降级占位，实际 %q", got)
	}
	if got := sa.AfterExecInfo(nil); got != "" {
		t.Errorf("AfterExecInfo 应返回空串，实际 %q", got)
	}
}

func TestSubAgentChildUsesWorkDirOverride(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("ok"), finishReason: sharedkernel.FinishReasonStop}},
	}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	// work_dir 入参覆盖构造时目录——子工具集以覆盖目录为工作区，
	// 由 child 注册表构造（bash/read_file）消费；无法直接观测目录，
	// 这里验证覆盖路径不报错且结果协议正常即可
	sa := newTestSubAgent(parent, "/default/wd")
	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t","work_dir":"/custom/wd"}`))
	if err != nil {
		t.Fatalf("work_dir 覆盖执行失败：err=%v", err)
	}
	var res SubAgentResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("应返回合法 JSON：%v\n%s", err, out)
	}
	if res.Report != "ok" || res.Status != SubAgentStatusComplete {
		t.Fatalf("结果不符：%+v", res)
	}
}

func TestSubAgentWithParentSessionRepo(t *testing.T) {
	// 显式验证 child 复用父 SessRepo：写出的消息能被同一 repo 检索到
	repo := newMemRepo()
	sess := newTestSession("parent-1", repo)
	parent := NewReActService(sess, repo, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("result-1")}},
	}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	sa := newTestSubAgent(parent, "/wd")
	if _, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t","work_dir":"/wd"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	contextSnapshot, err := repo.GetRequestContext(context.Background(), "parent-1")
	if err != nil {
		t.Fatalf("GetRequestContext: %v", err)
	}
	// 父会话自身应只有 system 一条
	if len(contextSnapshot.Messages) != 1 || contextSnapshot.Messages[0].Role != sharedkernel.RoleSystem {
		t.Errorf("父会话不应被 child 写入，实际 %+v", contextSnapshot.Messages)
	}
}

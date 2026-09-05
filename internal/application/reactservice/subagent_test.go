package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestSubAgentNameAndDefinition(t *testing.T) {
	parent := &ReActService{}
	sa := NewSubAgent(parent, "/tmp/wd")
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
	parent := NewReActService(sess, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)
	sa := NewSubAgent(parent, "/tmp/wd")

	_, err := sa.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err == nil || !strings.Contains(err.Error(), "parsing sub-agent args") {
		t.Fatalf("非法 JSON 应返回解析错误，实际 %v", err)
	}
}

func TestSubAgentExecuteMissingTask(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	parent := NewReActService(sess, &scriptedLLM{}, tools.NewDefaultRegistry(nil), nil, nil)
	sa := NewSubAgent(parent, "/tmp/wd")

	_, err := sa.Execute(context.Background(), json.RawMessage(`{"abstract":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "sub-agent task required") {
		t.Fatalf("缺 task 应报错，实际 %v", err)
	}
}

func TestSubAgentExecuteHappyPath(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsg("child result")},
	}}
	parent := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), nil, nil)
	sa := NewSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"count files","abstract":"counting","work_dir":"/tmp/wd"}`))
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	if out != "child result" {
		t.Errorf("应返回子 Agent 结论文本，实际 %q", out)
	}
	// 父会话不受影响（子会话独立、绝不写回）
	if len(sess.Messages) != 1 {
		t.Errorf("父会话不应被写入，实际 %d 条：%+v", len(sess.Messages), sess.Messages)
	}
	// 子会话落在独立 ID（sub: 前缀）下，repo 中可读到
	// system + user + assistant 共三条（Run 结束后结论一并持久化）
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
	repo.mu.Unlock()
	if total != 3 {
		t.Errorf("子会话应持久化三条消息（system/user/assistant），实际 %d", total)
	}
	if roles[sharedkernel.RoleSystem] != 1 || roles[sharedkernel.RoleUser] != 1 ||
		roles[sharedkernel.RoleAssistant] != 1 {
		t.Errorf("子会话消息角色分布不符：%v", roles)
	}
}

func TestSubAgentExecuteChildFailureReturnsString(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("parent", repo)
	llm := &scriptedLLM{responses: []scriptedResp{
		{err: errors.New("child llm failed")},
	}}
	parent := NewReActService(sess, llm, tools.NewDefaultRegistry(nil), nil, nil)
	sa := NewSubAgent(parent, "/tmp/wd")

	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if err != nil {
		t.Fatalf("子 Agent 内部失败不应中断父循环（error 应为 nil），实际 %v", err)
	}
	if !strings.Contains(out, "sub agent failed") || !strings.Contains(out, "child llm failed") {
		t.Errorf("失败原因应以工具结果字符串返回，实际 %q", out)
	}
}

func TestSubAgentBeforeAfterExecInfo(t *testing.T) {
	sa := NewSubAgent(&ReActService{}, "/tmp/wd")

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
	parent := NewReActService(sess, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("ok")}},
	}, tools.NewDefaultRegistry(nil), nil, nil)
	// work_dir 入参覆盖构造时目录——子工具集以覆盖目录为工作区，
	// 由 child 注册表构造（bash/read_file）消费；无法直接观测目录，
	// 这里验证覆盖路径不报错即可
	sa := NewSubAgent(parent, "/default/wd")
	out, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t","work_dir":"/custom/wd"}`))
	if err != nil || out != "ok" {
		t.Fatalf("work_dir 覆盖执行失败：out=%q err=%v", out, err)
	}
}

func TestSubAgentWithParentSessionRepo(t *testing.T) {
	// 显式验证 child 复用父 Repo：写出的消息能被同一 repo 检索到
	repo := newMemRepo()
	sess := newTestSession("parent-1", repo)
	parent := NewReActService(sess, &scriptedLLM{
		responses: []scriptedResp{{msg: assistantMsg("result-1")}},
	}, tools.NewDefaultRegistry(nil), nil, nil)
	sa := NewSubAgent(parent, "/wd")
	if _, err := sa.Execute(context.Background(), json.RawMessage(`{"task":"t","work_dir":"/wd"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msgs, err := repo.GetMessages(context.Background(), "parent-1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	// 父会话自身应只有 system 一条
	if len(msgs) != 1 || msgs[0].Role != sharedkernel.RoleSystem {
		t.Errorf("父会话不应被 child 写入，实际 %+v", msgs)
	}
}

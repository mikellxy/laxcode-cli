package llmprovider

import (
	"encoding/json"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestNewOpenApiProvider(t *testing.T) {
	// 仅验证构造装配，不发起任何网络请求
	p := NewOpenApiProvider("sk-test", "https://example.com/v1", "gpt-x")
	if p == nil {
		t.Fatal("NewOpenApiProvider 返回 nil")
	}
	if p.model != "gpt-x" {
		t.Errorf("model 未装配，实际 %q", p.model)
	}
}

// paramsJSON 把 buildResponseParams 的结果序列化为通用结构便于断言
// （不依赖 openai SDK 内部类型，仅依赖其公开 JSON 语义）。
func paramsJSON(t *testing.T, p *OpenApiProvider, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition) map[string]any {
	t.Helper()
	params := p.buildResponseParams(msgs, defs)
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal response params: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal response params: %v", err)
	}
	return m
}

func inputItems(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["input"].([]any)
	if !ok {
		t.Fatalf("params 应包含 input 数组，实际 %v", m)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		item, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("input 元素应为对象，实际 %T", r)
		}
		items = append(items, item)
	}
	return items
}

func TestBuildResponseParamsBasicMapping(t *testing.T) {
	p := &OpenApiProvider{model: "gpt-test"}
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: "you are laxcode"},
		{Role: sharedkernel.RoleUser, Content: "hi"},
	}
	m := paramsJSON(t, p, msgs, nil)
	if m["model"] != "gpt-test" {
		t.Errorf("model 不符：%v", m["model"])
	}
	items := inputItems(t, m)
	if len(items) != 2 {
		t.Fatalf("应映射 2 条输入，实际 %d：%v", len(items), items)
	}
	// message 类 item 无显式 type，以 role/content 断言
	if items[0]["role"] != "system" || items[0]["content"] != "you are laxcode" {
		t.Errorf("system 映射不符：%v", items[0])
	}
	if items[1]["role"] != "user" || items[1]["content"] != "hi" {
		t.Errorf("user 映射不符：%v", items[1])
	}
	if _, hasType := items[0]["type"]; hasType {
		t.Errorf("message item 不应携带 type 字段：%v", items[0])
	}
	// 无工具定义时不应生成 tools
	if _, has := m["tools"]; has {
		t.Errorf("无工具定义时不应包含 tools：%v", m["tools"])
	}
}

func TestBuildResponseParamsAssistantTurnOrdering(t *testing.T) {
	// assistant 消息含 reasoning + 正文 + 工具调用：同一轮内
	// reasoning item 必须排在最前，随后 message 与 function_call
	p := &OpenApiProvider{model: "m"}
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleAssistant, Content: "need to check",
			ReasoningID: "rsn-1", ReasoningContent: "第一步思考",
			ToolCalls: []sharedkernel.ToolCall{
				{ID: "call-a", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
				{ID: "call-b", Name: "read_file", Arguments: json.RawMessage(`{"path":"x"}`)},
			}},
	}
	items := inputItems(t, paramsJSON(t, p, msgs, nil))
	// 期望：user → reasoning → assistant message → function_call × 2
	if len(items) != 5 {
		t.Fatalf("assistant 轮应拆成 reasoning+message+function_call，整体 5 段，实际 %d：%v", len(items), items)
	}
	if items[1]["type"] != "reasoning" {
		t.Fatalf("第 2 个 item 应为 reasoning，实际 %v", items[1])
	}
	if items[1]["id"] != "rsn-1" {
		t.Errorf("reasoning id 不符：%v", items[1])
	}
	content, _ := items[1]["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("reasoning content 数组不符：%v", items[1])
	}
	firstText := content[0].(map[string]any)
	if firstText["text"] != "第一步思考" || firstText["type"] != "reasoning_text" {
		t.Errorf("reasoning text 不符：%v", firstText)
	}

	if items[2]["role"] != "assistant" || items[2]["content"] != "need to check" {
		t.Errorf("assistant 正文消息不符：%v", items[2])
	}
	if items[3]["type"] != "function_call" || items[3]["name"] != "bash" {
		t.Errorf("function_call 映射不符：%v", items[3])
	}
	if items[4]["type"] != "function_call" || items[4]["name"] != "read_file" {
		t.Errorf("第二个 function_call 映射不符：%v", items[4])
	}
}

func TestBuildResponseParamsMultipleToolCalls(t *testing.T) {
	p := &OpenApiProvider{model: "m"}
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleAssistant,
			ToolCalls: []sharedkernel.ToolCall{
				{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
				{ID: "call-2", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)},
			}},
	}
	items := inputItems(t, paramsJSON(t, p, msgs, nil))
	if len(items) != 3 {
		t.Fatalf("两个工具调用应各映射为 function_call item，实际 %d：%v", len(items), items)
	}
	names := []string{}
	for _, it := range items[1:] {
		if it["type"] != "function_call" {
			t.Errorf("期望 function_call，实际 %v", it)
		}
		names = append(names, it["name"].(string))
	}
	if names[0] != "bash" || names[1] != "read_file" {
		t.Errorf("工具调用顺序不符：%v", names)
	}
	if items[1]["call_id"] != "call-1" || items[1]["arguments"] != `{"command":"ls"}` {
		t.Errorf("第一个 function_call 内容不符：%v", items[1])
	}
}

func TestBuildResponseParamsToolResultMapping(t *testing.T) {
	p := &OpenApiProvider{model: "m"}
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleTool, ToolCallID: "call-1", Content: "ls output"},
	}
	items := inputItems(t, paramsJSON(t, p, msgs, nil))
	if len(items) != 1 {
		t.Fatalf("tool 消息应映射为 function_call_output，实际 %d：%v", len(items), items)
	}
	it := items[0]
	if it["type"] != "function_call_output" || it["call_id"] != "call-1" || it["output"] != "ls output" {
		t.Errorf("tool 结果映射不符：%v", it)
	}
}

func TestBuildResponseParamsToolsDefinition(t *testing.T) {
	p := &OpenApiProvider{model: "m"}
	defs := []sharedkernel.ToolDefinition{
		{Name: "bash", Description: "run bash", Parameters: map[string]any{"type": "object"}},
		{Name: "read_file", Description: "read file"},
	}
	m := paramsJSON(t, p, nil, defs)
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("应映射 2 个工具，实际 %v", m["tools"])
	}
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "bash" || tool0["description"] != "run bash" ||
		tool0["type"] != "function" || tool0["strict"] != true {
		t.Errorf("工具定义映射不符：%v", tool0)
	}
	if params, _ := tool0["parameters"].(map[string]any); params["type"] != "object" {
		t.Errorf("工具参数应透传：%v", tool0["parameters"])
	}
}

func TestBuildResponseParamsAssistantWithReasoningOnly(t *testing.T) {
	// reasoning 存在但正文为空：message item 应省略（仅 reasoning item）
	p := &OpenApiProvider{model: "m"}
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleAssistant, ReasoningID: "r2", ReasoningContent: "纯思考"},
	}
	items := inputItems(t, paramsJSON(t, p, msgs, nil))
	if len(items) != 1 || items[0]["type"] != "reasoning" {
		t.Fatalf("无正文的 assistant 应仅产出 reasoning item，实际 %v", items)
	}
}

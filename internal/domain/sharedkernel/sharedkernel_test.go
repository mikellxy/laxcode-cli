package sharedkernel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleConstants(t *testing.T) {
	if RoleSystem != "system" || RoleUser != "user" ||
		RoleAssistant != "assistant" || RoleTool != "tool" {
		t.Fatalf("角色常量与字符串不符: %q/%q/%q/%q", RoleSystem, RoleUser, RoleAssistant, RoleTool)
	}
}

func TestTokenStatisticsJSONRoundTrip(t *testing.T) {
	ts := TokenStatistics{TokenInput: 123, TokenOutput: 45}
	data, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back TokenStatistics
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != ts {
		t.Errorf("round trip 不一致：got %+v want %+v", back, ts)
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	msg := Message{
		Role:             RoleAssistant,
		Content:          "hello",
		ReasoningID:      "rsn-1",
		ReasoningContent: "thinking...",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
		},
		TokenUsed: TokenStatistics{TokenInput: 10, TokenOutput: 3},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Role != msg.Role || back.Content != msg.Content ||
		back.ReasoningID != msg.ReasoningID || back.ReasoningContent != msg.ReasoningContent {
		t.Errorf("消息字段回读不符：%+v", back)
	}
	if len(back.ToolCalls) != 1 || back.ToolCalls[0].Name != "bash" ||
		string(back.ToolCalls[0].Arguments) != `{"command":"ls"}` {
		t.Errorf("tool_calls 回读不符：%+v", back.ToolCalls)
	}
	if back.TokenUsed != msg.TokenUsed {
		t.Errorf("token_used 回读不符：%+v", back.TokenUsed)
	}
}

func TestMessageOmitEmptyOptionalFields(t *testing.T) {
	// reasoning/tool 字段带 omitempty：零值时序列化应省略，token_used 恒输出
	msg := Message{Role: RoleUser, Content: "hi"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, absent := range []string{"reasoning_id", "reasoning_content", "tool_calls", "tool_call_id"} {
		if strings.Contains(s, absent) {
			t.Errorf("零值字段 %s 不应序列化，实际 JSON: %s", absent, s)
		}
	}
	if !strings.Contains(s, `"token_used"`) {
		t.Errorf("token_used 无 omitempty 应恒输出，实际 JSON: %s", s)
	}
	if !strings.Contains(s, `"role":"user"`) {
		t.Errorf("role 应序列化，实际 JSON: %s", s)
	}
}

func TestToolResultJSON(t *testing.T) {
	// Error 为 error 接口：非 nil 时标准库序列化为 {}，nil 时应省略逻辑由
	// 调用方保证；这里校验字段名与 round trip
	tr := ToolResult{Output: "out", IsError: true, ToolCallID: "tc-1"}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{`"output":"out"`, `"is_error":true`, `"tool_call_id":"tc-1"`} {
		if !strings.Contains(s, field) {
			t.Errorf("缺少字段 %s，实际 JSON: %s", field, s)
		}
	}
	var back ToolResult
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Output != tr.Output || back.IsError != tr.IsError || back.ToolCallID != tr.ToolCallID {
		t.Errorf("round trip 不符：%+v", back)
	}
}

func TestToolDefinitionJSON(t *testing.T) {
	td := ToolDefinition{
		Name:        "read_file",
		Description: "read a file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
	data, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ToolDefinition
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Name != td.Name || back.Description != td.Description {
		t.Errorf("字段回读不符：%+v", back)
	}
	if got := back.Parameters[ToolDefParamRequired]; got == nil {
		t.Error("parameters.required 应可回读")
	}
}

func TestToolDefParamConstants(t *testing.T) {
	if ToolDefParamProperties != "properties" || ToolDefParamRequired != "required" {
		t.Errorf("常量与 JSON key 不符")
	}
}

func TestChunkKindsMonotonic(t *testing.T) {
	// 保证枚举顺序稳定：start/delta/end 三段式先正文后 reasoning
	if ChunkTextStart != 0 || ChunkTextDelta != 1 || ChunkTextEnd != 2 {
		t.Error("ChunkText 枚举顺序被改动")
	}
	if ChunkReasoningStart != 3 || ChunkReasoningDelta != 4 || ChunkReasoningEnd != 5 {
		t.Error("ChunkReasoning 枚举顺序被改动")
	}
	if ChunkToolCall != 6 {
		t.Error("ChunkToolCall 枚举值被改动")
	}
}

func TestStreamChunkFieldAccess(t *testing.T) {
	tc := &ToolCall{ID: "c1", Name: "bash"}
	ch := StreamChunk{Kind: ChunkToolCall, ToolCall: tc}
	if ch.ToolCall != tc {
		t.Error("ToolCall 指针字段应原样保留")
	}
	delta := StreamChunk{Kind: ChunkTextDelta, Delta: "ab"}
	if delta.Delta != "ab" {
		t.Error("Delta 字段读写异常")
	}
}

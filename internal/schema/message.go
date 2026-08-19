package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词：确立 Agent 的人格与红线
	RoleUser      Role = "user"      // 用户输入，工具执行返回的结果
	RoleAssistant Role = "assistant" // 模型输出
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

const (
	ToolDefParamProperties = "properties"
	ToolDefParamRequired   = "required"
)

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
}

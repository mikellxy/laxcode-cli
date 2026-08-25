package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词：确立 Agent 的人格与红线
	RoleUser      Role = "user"      // 用户输入，工具执行返回的结果
	RoleAssistant Role = "assistant" // 模型输出
)

type TokenStatistics struct {
	TokenInput  int `json:"token_input"`
	TokenOutput int `json:"token_output"`
}

func (t *TokenStatistics) Total() int {
	return t.TokenInput + t.TokenOutput
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// TokenUsed 记录该消息产生时那次模型调用的 token 用量，raw 计费口径：
	// TokenInput 为本次请求发送的全部输入（含 system prompt 与当时全部历史），
	// TokenOutput 为本次响应输出。仅 assistant 消息携带非零值；
	// user/tool/system 消息恒为零值。序列化无 omitempty，历史文件每行恒输出。
	TokenUsed TokenStatistics `json:"token_used"`
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

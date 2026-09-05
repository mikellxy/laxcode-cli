package sharedkernel

const (
	RoleSystem    = "system"    // 系统提示词：确立 Agent 的人格与红线
	RoleUser      = "user"      // 用户输入，工具执行返回的结果
	RoleAssistant = "assistant" // 模型输出
	RoleTool      = "tool"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningID and ReasoningContent carry the model's chain-of-thought
	// (assistant messages only), replayed to Responses API on later turns.
	ReasoningID      string     `json:"reasoning_id,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	// TokenUsed 记录该消息产生时那次模型调用的 token 用量，raw 计费口径：
	// TokenInput 为本次请求发送的全部输入（含 system prompt 与当时全部历史），
	// TokenOutput 为本次响应输出。仅 assistant 消息携带非零值；
	// user/tool/system 消息恒为零值。序列化无 omitempty，历史文件每行恒输出。
	TokenUsed TokenStatistics `json:"token_used"`
}

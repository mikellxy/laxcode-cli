package sharedkernel

const (
	RoleSystem    = "system"    // 系统提示词：确立 Agent 的人格与红线
	RoleUser      = "user"      // 用户输入
	RoleAssistant = "assistant" // 模型输出
	RoleTool      = "tool"
)

type Message struct {
	// Seq 在 session 内单调递增；system 消息不属于追加流水，使用 0。
	Seq uint64 `json:"seq,omitempty"`
	// TurnID 标识一次模型响应，其工具结果继承相同 ID；user/system 为空。
	TurnID string `json:"turn_id,omitempty"`
	// 一条 assistant 发起的全部调用及其结果共享同一个调用组 ID。
	ToolCallGroupID string       `json:"tool_call_group_id,omitempty"`
	Artifact        *ArtifactRef `json:"artifact,omitempty"`
	Role            string       `json:"role"`
	Content         string       `json:"content"`
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

// ArtifactRef 指向当前 session 内的不可变工具输出，ID 为内容 SHA-256。
type ArtifactRef struct {
	ID       string `json:"id"`
	ByteSize int    `json:"byte_size"`
}

// Clone 隔离一条消息的可变字段；正文 string 可安全共享。
func (m Message) Clone() Message {
	m.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
	for i := range m.ToolCalls {
		m.ToolCalls[i].Arguments = append([]byte(nil), m.ToolCalls[i].Arguments...)
	}
	if m.Artifact != nil {
		ref := *m.Artifact
		m.Artifact = &ref
	}
	return m
}

// CloneMessages 隔离请求候选、持久化快照和运行中会话的可变字段。
func CloneMessages(msgs []Message) []Message {
	if msgs == nil {
		return nil
	}
	out := append([]Message(nil), msgs...)
	for i := range out {
		out[i] = msgs[i].Clone()
	}
	return out
}

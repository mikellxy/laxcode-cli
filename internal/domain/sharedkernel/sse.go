package sharedkernel

// ChunkKind 判别流式事件的语义。正文与 reasoning 采用 start/delta/end
// 三段式，与业界（Vercel AI SDK / AG-UI）惯例对齐；工具调用不流式，
// 参数攒齐后以单个 ChunkToolCall 事件推送。
type ChunkKind int

const (
	ChunkTextStart      ChunkKind = iota // 一段正文开始
	ChunkTextDelta                       // 正文增量
	ChunkTextEnd                         // 该段正文结束
	ChunkReasoningStart                  // 一段 reasoning（thinking）开始
	ChunkReasoningDelta                  // reasoning 增量
	ChunkReasoningEnd                    // 该段 reasoning 结束
	ChunkToolCall                        // 一个完整工具调用（参数已攒齐）
)

// StreamChunk 是 provider 层流式生成的领域级事件：只描述“一轮 LLM 生成”
// 内的增量（正文 / reasoning / 工具调用），与 HTTP/SSE 等传输协议无关，
// 由上层消费者（未来的 SSE handler）自行序列化。loop 级事件（run/step/
// tool-result 边界）不属于本类型，留给上层编排。
type StreamChunk struct {
	Kind     ChunkKind
	Delta    string    // *Delta 类事件：本次增量文本
	ToolCall *ToolCall // ChunkToolCall 事件：一个完整工具调用
}

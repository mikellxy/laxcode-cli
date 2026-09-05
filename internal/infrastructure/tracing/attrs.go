package tracing

import "go.opentelemetry.io/otel/attribute"

// span 名。DDD 架构调用树（去掉老 terminal-task 层，聚焦 ReAct 循环）：
// ReAct → llm-turn → {llm-generate, tool-exec}。ReAct/llm-turn 由
// ReActService 负责，tool-exec 由工具注册表负责，llm-generate 由 provider
// 层负责（本次不迁移）。
const (
	SpanReAct       = "ReAct"
	LLMTurn         = "llm-turn"
	SpanLLMGenerate = "llm-generate"
	SpanToolExec    = "tool-exec"
)

// laxcode 自有概念的业务属性键
const (
	AttrSessionID     attribute.Key = "laxcode.session_id"
	AttrToolName      attribute.Key = "laxcode.tool_name"
	AttrAgentRole     attribute.Key = "laxcode.agent_role"
	AttrTurnSeq       attribute.Key = "laxcode.loop_seq"
	AttrToolCallCount attribute.Key = "laxcode.tool_call_count"
	AttrTimeCostMs    attribute.Key = "laxcode.time_cost_ms"
)

// AttrAgentRole 的取值
const (
	AgentRoleMain = "main"
	AgentRoleSub  = "sub"
)

// token 用量属性键：键名对齐 OTel GenAI 语义约定（experimental），
// 使兼容后端可自动识别；以字面量常量定义而不引入 semconv 包，
// 约定演进时在此单点修改。
const (
	AttrInputTokens  attribute.Key = "gen_ai.usage.input_tokens"
	AttrOutputTokens attribute.Key = "gen_ai.usage.output_tokens"
)

// Package tracing 是 laxcode 的 OpenTelemetry 埋点语义约定与装配入口：
// 集中定义 span 名与属性键常量、提供 Tracer 派生与 noop 缺省、以及进程
// 退出前的 Shutdown 钩子。
//
// 产品只依赖 OTel API 模块，不提供任何真实上报后端的实现。用户接入方式：
// 自行实现 trace.TracerProvider 接口（其 Span.End 即上报触发点，每个 span
// 同步一次）并注入 New；或在使用方进程装配官方 SDK 后把
// otel.GetTracerProvider() 传入 New。批量与导出策略由实现方决定。
//
// 注意：OTel API 的 TracerProvider / Tracer / Span 均为密封接口（内嵌未导出
// 方法），自定义实现须按官方 "API Implementations" 约定内嵌
// go.opentelemetry.io/otel/trace/embedded 包中的对应接口来满足，
// 用法示例见本包及 internal/engine 的 tracing_test.go。
package tracing

import "go.opentelemetry.io/otel/attribute"

// span 名。调用树：terminal-task → agent-run → react-loop →
// {llm-generate, tool-exec}；one-shot 模式无 terminal-task 层，
// agent-run 直接作为 root。
const (
	SpanTerminalTask = "terminal-task"
	SpanAgentRun     = "agent-run"
	SpanReactLoop    = "react-loop"
	SpanLLMGenerate  = "llm-generate"
	SpanToolExec     = "tool-exec"
)

// laxcode 自有概念的业务属性键
const (
	AttrSessionID     attribute.Key = "laxcode.session_id"
	AttrToolName      attribute.Key = "laxcode.tool_name"
	AttrAgentRole     attribute.Key = "laxcode.agent_role"
	AttrLoopSeq       attribute.Key = "laxcode.loop_seq"
	AttrTaskSeq       attribute.Key = "laxcode.task_seq"
	AttrToolCallCount attribute.Key = "laxcode.tool_call_count"
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

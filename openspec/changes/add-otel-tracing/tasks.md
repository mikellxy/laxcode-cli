# Tasks: add-otel-tracing

## 1. 依赖与 tracing 包骨架

- [x] 1.1 引入 OTel API 依赖：`go get go.opentelemetry.io/otel go.opentelemetry.io/otel/trace`；确认 go.mod 不包含任何 `otel/sdk` 或 exporter 模块
- [x] 1.2 创建 `internal/tracing/attrs.go`：集中定义 span 名（terminal-task / agent-run / react-loop / llm-generate / tool-exec）与属性 key 常量（`laxcode.session_id`、`laxcode.tool_name`、`laxcode.agent_role`、`laxcode.loop_seq`、`laxcode.task_seq`、`laxcode.tool_call_count`，token/model 类采用 GenAI semconv 键名）
- [x] 1.3 创建 `internal/tracing/tracing.go`：Tracer 派生辅助（从 `trace.TracerProvider` 派生，nil provider → noop）、Shutdown 钩子（接口断言探测可选的 `Shutdown(context.Context) error`，noop 下为空操作）

## 2. 注入点

- [x] 2.1 `AgentEngine` 新增 `Tracer trace.Tracer` 字段，`NewAgentEngine` 加参且 nil 缺省为 noop tracer
- [x] 2.2 `tools.DefaultRegistry` 新增 tracer 字段，`NewDefaultRegistry` 加参且 nil 缺省为 noop tracer（与 printer 注入同姿势）

## 3. 埋点

- [x] 3.1 `Run`：开启 `agent-run` span（session_id、agent_role）；循环内累计 token，结束时设置合计属性；返回错误（含 too_many_turns）时 RecordError + SetStatus(Error)
- [x] 3.2 `Run` 循环每轮：开启 `react-loop` span（loop_seq）；Generate 前后包 `llm-generate` span（input/output token、tool_call_count；失败时 RecordError + SetStatus(Error)）
- [x] 3.3 `DefaultRegistry.Execute`：开启 `tool-exec` span（session_id 来自 ctx 侧已携带、tool_name；错误结果 RecordError + SetStatus(Error)）；不记录 token；确认 read_file 并行分支 goroutine 沿用传入 ctx，生成兄弟 span
- [x] 3.4 `TerminalLoop`：每次用户输入开启 `terminal-task` root span（新 trace、task_seq），Run 结束后以 session 累计 token 差值设置合计属性
- [x] 3.5 `OneShotLoop`：确认以 `agent-run` 为 root（不额外加层），task 追加进 session 后开启
- [x] 3.6 `cmd/main/main.go`：装配 tracing（默认 noop）并注入 engine 与 registry；交互模式 exit/EOF 与 one-shot 运行结束两条退出路径 defer Shutdown（one-shot 路径在写契约 JSON 之前执行 flush）

## 4. 测试

- [x] 4.1 `internal/tracing` 单测：nil provider → noop tracer；Shutdown 对无该方法的对象为空操作、对有该方法的对象正确转发
- [x] 4.2 以内存 TracerProvider 测试桩验证 span 树：terminal-task → agent-run → react-loop → llm-generate/tool-exec 的父子关系与属性（session_id、loop_seq、tool_name、token 值与 session 记账口径一致）
- [x] 4.3 验证并行 read_file 生成同一 react-loop 下的兄弟 span；子 Agent 的 agent-run（agent_role=sub）嵌套在 tool-exec(sub_agent) 下
- [x] 4.4 验证错误映射：工具失败仅 tool-exec 置错误状态；生成失败时 llm-generate 与 agent-run 均置错误状态
- [x] 4.5 全量回归 `go test ./...`（现有测试不传 tracer 即 nil → noop，行为不变）；`go mod tidy` 后确认无 sdk/exporter 依赖

## 5. 文档与约定

- [x] 5.1 更新 `openspec/config.yaml` 的 context：internal 子包列表加入 `tracing = OTel 埋点语义约定、Tracer 注入与生命周期钩子`
- [x] 5.2 在 `internal/tracing` 包注释中说明用户接入方式（实现 `trace.TracerProvider` 注入，或装配官方 SDK 后注入全局 provider）与"产品不提供真实上报实现"的 边界

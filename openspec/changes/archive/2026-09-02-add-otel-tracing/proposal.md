# Proposal: add-otel-tracing

## Why

laxcode 的运行过程（react loop、LLM 生成、工具执行）目前只有 session 本地
token 记账（meta.json），缺乏时序与调用层级视角，无法回答"这一轮任务慢在
哪次 generate / 哪个工具"这类问题，也无法接入任何标准监控后端。
需要引入分布式追踪能力，但以"默认零开销、用户可插拔"的方式落地：
产品本体不提供真实上报实现，只定义埋点与注入点。

## What Changes

- 新增 `internal/tracing` 包：集中定义 span 名与属性 key 常量（本地语义
  约定）、Tracer 注入辅助与 noop 默认值、进程退出前的 `Shutdown(ctx)`
  生命周期钩子。
- 仅引入 OpenTelemetry **API 模块**依赖（`go.opentelemetry.io/otel`、
  `go.opentelemetry.io/otel/trace`），不引入 SDK 模块；未注入真实
  TracerProvider 时全部走 OTel 官方 noop 实现，零行为变化。
- `AgentEngine` 与 `tools.DefaultRegistry` 以策略模式注入
  `trace.Tracer`（结构体字段，构造期传入，缺省 noop），与现有
  Printer/MonitoredProvider 注入哲学一致。
- span 树埋点（父子关系经 `context.Context` 传播，现有调用链已贯通）：
  - 交互模式：`TerminalLoop` 每次用户输入开启一个 trace，root 为
    `terminal-task` span（记录该次输入任务的耗时与 token 合计）；
  - one-shot 模式：以 `agent-run` 为 root，无额外层；
  - `Run` 生成 `agent-run` span；每轮外层 for 循环生成 `react-loop`
    span（携带 loop 序号），其下生成 `llm-generate` span（记录耗时、
    input/output token、工具调用数）；
  - `Registry.Execute` 每次工具执行生成 `tool-exec` span（携带
    `tool_name`，只记耗时与错误状态，不记 token）；
  - SubAgent 作为工具执行，其 `agent-run` 子树经 ctx 自动嵌套在
    对应 `tool-exec` span 下，以 `agent_role` 属性区分 main/sub。
- 所有 span 携带 `session_id` 业务键；唯一标识使用 OTel 实现侧生成的
  trace_id/span_id/parent_span_id，不自定义 ID 体系。
- main 的两条退出路径（交互模式 exit/EOF、one-shot 运行结束）defer
  调用 tracing 的 `Shutdown`，保证注入真实 SDK 时批量导出不丢尾部 span
  （one-shot 任务存活时间可能短于批量导出间隔）。
- 更新 `openspec/config.yaml` 架构约定，将 `tracing` 加入 internal
  子包列表。
- 用户扩展方式：自行实现 OTel API 的 `trace.TracerProvider` 接口并注入，
  或在使用方进程中装配官方 SDK 后 `otel.SetTracerProvider`；上报时机即
  实现侧 `Span.End()`（每 span 同步一次），批量与导出策略由实现方决定。
  本产品不提供任何真实上报后端的实现。

## Capabilities

### New Capabilities

- `tracing/otel-tracing`: 基于 OpenTelemetry API 的可插拔分布式追踪——
  span 树结构（terminal-task / agent-run / react-loop / llm-generate /
  tool-exec）、属性规范（session_id、tool_name、token、耗时）、
  Tracer 策略注入与 noop 默认、Shutdown 生命周期钩子。

### Modified Capabilities

<!-- 无既有 spec 的需求级变更：engine/tools 的改动是新增观测埋点，
     不改变现有功能行为；token-usage（session 本地记账）保持不变，
     与 tracing 是两条独立通路。 -->

## Impact

- **新增依赖**：`go.opentelemetry.io/otel`、`go.opentelemetry.io/otel/trace`
  （仅 API 模块，无网络/Exporter 依赖）。
- **改动代码**：
  - `internal/tracing/`（新包）
  - `internal/engine/engine.go`（TerminalLoop / Run 埋点、AgentEngine
    新增 Tracer 字段）
  - `internal/engine/oneshot.go`（OneShotLoop root span）
  - `internal/engine/toolcall.go`（executeToolCalls 传 ctx 不变，
    并行 read_file 分支的 goroutine 沿用同一 ctx）
  - `internal/tools/registry.go`（DefaultRegistry 新增 tracer 字段与
    Execute 埋点）
  - `cmd/main/main.go`（装配 tracer、两条退出路径 defer Shutdown）
- **行为兼容性**：默认 noop，无 TracerProvider 注入时行为与性能开销
  与现状一致；所有现有测试不需要感知 tracing。
- **文档/约定**：`openspec/config.yaml` 的 internal 子包列表加入 tracing。

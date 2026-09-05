## Why

provider 层的流式底座（`StreamProvider.GenerateStream` + `StreamChunk`）已在 `2026-09-02-add-openai-streaming` 落地，但至今无消费者——它在 proposal 的「已知后续」和 design 决策 3/4 中明确点名了本次要补的第②层：engine 的 `RunSse` 循环与 HTTP SSE handler。本 change 兑现这块预留能力，为 laxcode 增加第三种交互模式（terminal / one-shot 之外）：客户端经 HTTP POST 发起任务，服务端边跑 ReAct 循环边以 SSE 流式吐增量。同时清掉两处历史包袱——`MonitoredProvider` 装饰器（它使 `f.Provider` 无法断言到 `StreamProvider`，是 SSE 的直接阻塞点）与早已失效的 turn 上限残留（`errTooManyTurns` 自 `bash-tool-bg-process-safety` 移除上限后已成死代码，但 live spec 仍把它写成契约）。

## What Changes

- 新增 engine 层 `RunSse`：`Run` 的兄弟驱动函数，复用既有原语（`executeToolCalls`、`buildToolResultContent`、`SimpleCompactor.Compress`、`sess.Append`、tracing helpers），仅把「调 `Generate` + `Printer.PrintLLM` 整条批式输出」替换为「调 `GenerateStream` + emit loop 级事件」。`RunSse` 不触碰 `Printer`。
- 新增 engine 层事件词汇 `RunEvent`（layer②）：套 `run-start` / `step-start` / `tool-result` / `run-finish` / `run-error` 边界，并把 provider 的 `StreamChunk`（layer①）透传翻译为 `text-*` / `reasoning-*` / `tool-call` 事件。事件与传输无关，SSE 帧格式由 handler 负责。
- 新增 HTTP SSE server：`main.go` 检测 `-sse` 开关，置位则只启动 http server（不进 REPL）。`POST` 请求体携带 `session_id` 与 `prompt`，handler 每请求 `GetSession` + `assembleEngine`（不预注入 session），以 `data:{"type":...}` 单行 JSON 帧 + `Flush` 流式回吐 `RunSse` 事件。
- **BREAKING**（内部）：移除 `MonitoredProvider` 装饰器，token 记账（`sess.RecordGenerate`）内联回 ReAct 循环（`Run` 与 `RunSse`）。吸纳 DDD 分层——Engine 与 Session 是两个 domain，ReAct loop 是 application 层负责编排二者。此重构 spec 中立（token-usage spec 机制无关），且解除 SSE 的断链阻塞。
- 清理 turn 上限残留：删除死代码 `errTooManyTurns` 及其在 `TerminalLoop` / one-shot 错误映射中的分支；**同步修改两处 live spec** 移除 `too_many_turns` 契约描述（已归档工件按约定不动）。
- SSE 模式默认静默：server 启动即 `printer.SetDefault(DiscardPrinter{})`，`RunSse` 与 Registry 的中间过程输出全部落空，SSE 流是唯一输出通道。

## Capabilities

### New Capabilities

- `engine/run-sse`: engine 层的流式 ReAct 编排能力——`RunSse` 循环契约、`RunEvent` 事件词汇与 layer①/② 分层、`StreamChunk` 透传翻译、与批式 `Run` 的输出等价性、provider 非流式时的批式降级、以及记账内联（`RecordGenerate` 由循环直接调用）。
- `engine/sse-http-server`: SSE 交互模式——`-sse` 开关与「只起 server」语义、POST 请求契约（`session_id` + `prompt`）、每请求 `GetSession` + 引擎装配、SSE 帧格式（`data:` 单行 JSON）与 flush、静默输出、workDir 固定为启动目录、server 级 tracer 复用、客户端断连经 ctx 取消传播、优雅关停。

### Modified Capabilities

- `engine/one-shot`: 错误分类移除 `too_many_turns`（turn 上限已废，`error.type` 仅剩 `usage` / `generate`），并删除对应「工具循环超限」Scenario。
- `engine/session`: ①移除「会话内消息追加」中依赖 turn 上限中断的留痕子句与 Scenario；②放宽「Session 初始化与加载」的单会话约束以支持 SSE 模式按请求惰性加载多个 session（仍不扫描目录），并把 HTTP 请求体列为 `session_id` 来源之一；③新增 SessionDB 并发访问保护要求（读写锁守护 map，跨会话安全；同一 session 的并发轮次不在本版支持范围）。

## Impact

- **代码（engine）**：新增 `internal/engine/runsse.go`（`RunSse` + `RunEvent`）、`internal/engine/sse_server.go`（HTTP handler + server 装配）；改 `reactservice.go`（`Run` 内联记账、删 `errTooManyTurns`、简化 `TerminalLoop` 错误分支）、`oneshot.go`（删 `ErrTypeTooManyTurns` 及映射）、`session.go`（`SessionDB` 加读写锁、`GetSession` 双检加载、文档注释更新）；删 `monitored_provider.go` 与 `monitored_provider_test.go`。
- **代码（main）**：`cmd/main/main.go` 新增 `-sse`（及 `-addr`）参数与 `runSseServer` 装配路径；`assembleEngine` 改用裸 provider（去 `MonitoredProvider`）。
- **代码（subagent）**：`internal/engine/subagent.go` 装配改用裸 provider。
- **依赖**：仅标准库 `net/http`，无新增第三方依赖。
- **spec**：新增 2 个 capability，修改 2 个 live capability（one-shot / session）；已归档 change 工件不改。
- **不受影响**：`provider` 包（layer① 已就绪，零改动）、`AnthropicProvider`、terminal / one-shot 的既有可观测行为（除 `too_many_turns` 契约移除）、`Printer` 接口。
- **已知后续（不在本次实现）**：同一 `session_id` 的并发轮次安全（本版由客户端串行化，SessionDB 锁只保证跨会话不崩溃）；per-session tracing 落盘（本版 server 级单 tracer 复用）；SSE 端点的鉴权 / 限流 / 多 workDir。

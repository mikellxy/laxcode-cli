## Why

LaxCode 的 provider 层目前只有批式 `Generate`：阻塞直到模型生成完毕，一次性返回完整 `*schema.Message`。我们计划在 terminal、one-shot 之外新增第三种交互模式——HTTP SSE 流式模式，让客户端边生成边接收增量。这需要 provider 具备"边生成边吐事件"的能力。本 change 在 provider 层铺设这块底层能力，作为未来 SSE 模式的依赖，且完全不触碰现有批式链路。

## What Changes

- 新增可选接口 `StreamProvider`（`internal/provider`），方法 `GenerateStream(ctx, msgs, tools, emit func(StreamChunk)) (*schema.Message, error)`。主接口 `Provider` 不变。
- 新增领域级流式事件类型 `StreamChunk`：判别式 `Kind` + start/delta/end 三段式粒度，对齐业界（Vercel AI SDK / AG-UI）惯例，HTTP/SSE 无关。
- `OpenApiProvider` 实现 `GenerateStream`：用 openai-go/v3 的 `Responses.NewStreaming` 消费 SSE 事件，一边累积出与批式 `Generate` 等价的完整 `*schema.Message`（含正文、reasoning、工具调用、token 用量），一边通过 `emit` 实时吐增量。
- 工具调用**不流式**：函数参数 delta 仅在内部按 item 累积，待输出项完成时 emit 一个完整工具调用事件。
- token 用量只在终止事件（`response.completed`）可得，累积进返回消息的 `TokenUsed`。
- 现有 `Provider` 主接口、批式 `Generate`、`AnthropicProvider`、terminal / one-shot / subagent / printer 全部**零改动**：不实现 `StreamProvider` 即天然降级到批式。

## Capabilities

### New Capabilities

- `provider/streaming`: provider 层的流式生成能力——`StreamProvider` 接口契约、`StreamChunk` 事件词汇与三段式粒度、增量累积与 emit 语义、终止用量、上下文取消与错误传播，以及与批式 `Generate` 的输出等价性。

### Modified Capabilities

<!-- 无：批式 Generate 的既有行为不变，现有 spec 无需求级变更。 -->

## Impact

- **代码**：`internal/provider/interface.go`（新增 `StreamProvider` 接口与 `StreamChunk` 类型）、`internal/provider/openai.go`（新增 `GenerateStream` 方法及其事件累积逻辑）。
- **依赖**：复用已有 `github.com/openai/openai-go/v3` 的 `Responses.NewStreaming` 与 `ssestream`，无新增第三方依赖。
- **不受影响**：`AnthropicProvider`（不实现即降级）、`engine.Run`、`Printer`、terminal / one-shot / subagent 前端。
- **已知后续（不在本次实现）**：
  - `MonitoredProvider` 装饰器目前不转发 `GenerateStream`；因其包裹后类型断言到 `StreamProvider` 会失效，需在未来接入消费者时补转发并定义流式下的 token 记账口径。
  - engine 层 `RunSse` / `SSELoop`（消费 `GenerateStream` 并叠加 run/step/tool-result 等 loop 级事件）与 HTTP SSE handler，另立 change 实现。

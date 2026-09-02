## Context

现有 provider 层是批式：`Provider.Generate(ctx, msgs, tools) (*schema.Message, error)` 阻塞至生成完毕再一次性返回。`engine.Run` 是共享的 agent-loop 核心，被三个前端复用——`TerminalLoop`、`OneShotLoop`、`SubAgent.Execute`——三者都是**批式输出**模式，差异仅在输出目的地（通过注入不同 `Printer` 实例）与前端包装。`Run` 的输出全部经 `f.Printer`（如 `PrintLLM(msg)` 整条打印），不直接触达 stdout。

约束（详见 proposal.md - Why / Impact）：

- 本次只在 provider 层加流式能力，terminal / one-shot / subagent / printer **零改动**，批式 `Generate` 行为不变。
- `openai-go/v3`（已在用）的 Responses API 提供 `Responses.NewStreaming(ctx, params)`，返回 `*ssestream.Stream[ResponseStreamEventUnion]`；Responses API **无内置 accumulator**（SDK 根目录的 `ChatCompletionAccumulator` 只服务 Chat Completions），累积需自行处理。
- provider 是叶子包，事件类型不得引入 HTTP/SSE 概念。

## Goals / Non-Goals

**Goals:**

- 定义 provider 层流式契约：可选接口 `StreamProvider` + 领域事件 `StreamChunk`，与传输协议解耦。
- `OpenApiProvider.GenerateStream` 累积出与批式 `Generate` **语义等价**的完整消息，同时经 `emit` 实时吐增量。
- 事件粒度对齐业界（start/delta/end 三段式），使未来 HTTP handler 退化为薄序列化层，不返工。

**Non-Goals:**

- 不实现 engine 层 `RunSse` / `SSELoop`、HTTP SSE handler、SSE 帧格式（另立 change）。
- 不改 `AnthropicProvider`、`MonitoredProvider`、`Printer`、任何前端 loop。
- 不定义 loop 级事件（run/step/tool-result 边界）——那是未来第②层的职责。

## Decisions

### 决策 1：emit 回调 + 可选 `StreamProvider` 接口（方案 A）

主接口 `Provider` 不动，新增可选能力接口；消费者用类型断言探测，未实现者自动降级到批式。

```go
type StreamProvider interface {
    Provider
    GenerateStream(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition,
        emit func(StreamChunk)) (*schema.Message, error)
}
```

- **为何不用 channel（方案 B）**：channel 需自管 goroutine 生命周期、ctx 取消、关闭与错误回传，对当前顺序阻塞的 `Run` 心智负担大；回调无 goroutine、错误传播天然。
- **为何不把 sink 注入 provider 构造器（方案 C）**：会让 provider 反向依赖 Printer/输出层，打破叶子包洁净，且 one-shot/subagent 的静默注入时机变复杂。
- **为何用可选接口而非改主接口**：`AnthropicProvider` 与现有测试无需实现即可继续工作，改动面收敛到 provider 包。

### 决策 2：`StreamChunk` 采用 start/delta/end 三段式（方案 Y）

```go
type ChunkKind int

const (
    ChunkTextStart ChunkKind = iota // 一段正文开始
    ChunkTextDelta                  // 正文增量
    ChunkTextEnd                    // 该段正文结束
    ChunkReasoningStart             // 一段 reasoning 开始
    ChunkReasoningDelta             // reasoning 增量
    ChunkReasoningEnd               // 该段 reasoning 结束
    ChunkToolCall                   // 一个完整工具调用（参数已攒齐）
)

type StreamChunk struct {
    Kind     ChunkKind
    Delta    string           // *Delta 类：增量文本
    ToolCall *schema.ToolCall // ChunkToolCall：完整调用
}
```

- **为何不用光 delta（方案 X）**：X 下消费者需靠 `Kind` 切换推断一段内容的起止，前端难以在 reasoning 结束时折叠 thinking。Y 与 Vercel AI SDK（`text-start/-delta/-end`、`reasoning-start/-delta/-end`）、AG-UI（`TEXT_MESSAGE_START/CONTENT/END`、`REASONING_*`）1:1，未来 handler 近乎透传，接现成前端生态省事。
- **成本**：事件种类翻倍，但 OpenAI Responses 免费提供边界信号（见决策 5），累积逻辑增量很小。

### 决策 3：两层协议红线

provider 的 `StreamChunk` **只描述一轮 LLM 生成**（正文 / reasoning / 工具调用）。loop 级事件（`RUN_*` / `STEP_*` / `TOOL_RESULT`）属于未来 engine 层第②层，**不得**下沉进 provider。这样 provider 保持叶子包，未来加 HTTP loop 不返工。

```
第①层 provider.GenerateStream → StreamChunk（本 change）
第②层 engine.RunSse/SSELoop  → 套 run/step/tool-result/finish 边界，转 SSE 帧（未来 change）
```

### 决策 4：`Run` vs `RunSse` —— 未来单独写 `RunSse`（本次不实现，仅定向）

判断准则：**同算法 + 不同目的地** → 注入 sink 共用一个函数（`Printer` 之于 terminal/oneshot/subagent）；**同算法 + 不同输出形状**（批式 vs 流式）→ 拆成共享原语的兄弟驱动函数。

SSE 属"不同输出形状"：`Run` 经 `Printer.PrintLLM(msg)` 整条批式输出，Printer 抽象的是目的地而非粒度。强行让 `Run` 吐 SSE 只有两条坏路——(a) `Run` 永远调 `GenerateStream`（违反零改动约束、terminal 白担流式开销），(b) `Run` 内部 `if 流式` 分叉（循环体照样裂两份）。故未来新增 `RunSse`，复用既有原语（`executeToolCalls`、`buildToolResultContent`、`SimpleCompactor.Compress`、`sess.Append`、tracing helpers），仅替换"调 `GenerateStream` + emit SSE 事件"这一层。否决"泛化 Printer 为统一 Emitter、单一 Run 永远流式"方案：会污染 Printer、迫使 terminal/oneshot 实现用不上的增量方法并承担流式开销。

### 决策 5：SDK 流式事件 → 累积 / emit 映射

遍历模式：`for stream.Next() { switch ev := stream.Current(); ev.Type { ... } }`，循环后检查 `stream.Err()`，`defer stream.Close()`。请求构造逻辑完全复用现有 `Generate`（`responses.ResponseNewParams`）。

| OpenAI Responses 事件 | 累积到返回 msg | emit 的 StreamChunk |
|---|---|---|
| `ResponseTextDeltaEvent.Delta` | `msg.Content += Delta` | 首个 delta 前补 `ChunkTextStart`，再 `ChunkTextDelta{Delta}` |
| `ResponseTextDoneEvent` / 文本项 `OutputItemDone` | — | `ChunkTextEnd`（若曾 start） |
| `ResponseReasoningTextDeltaEvent.Delta` / `ResponseReasoningSummaryTextDeltaEvent.Delta` | 不累积（仅供实时显示） | 首个前补 `ChunkReasoningStart`，再 `ChunkReasoningDelta{Delta}` |
| reasoning 项 `OutputItemDone` | `msg.ReasoningID = item.ID`；`msg.ReasoningContent = 拼接 item.AsReasoning().Content[].Text`（原始思考，与批式同源；不含 summary / encrypted） | `ChunkReasoningEnd`（若曾 start） |
| `ResponseFunctionCallArgumentsDeltaEvent` | 不累积（忽略参数片段，完整参数取自下一行 `OutputItemDone`） | 不 emit（半成品参数无意义） |
| function_call 项 `OutputItemDone`（含完整 `Arguments`/`CallID`/`Name`） | append 到 `msg.ToolCalls` | `ChunkToolCall{&tc}` |
| `ResponseCompletedEvent.Response.Usage` | `msg.TokenUsed`（输入/输出） | 不 emit（用量随返回值交付） |

**边界推导（决策 2 的 start/end 从哪来）**：采用"**首个 delta 惰性触发 start（每段一次，用 bool 标记）、对应 done 事件触发 end**"，而非依赖 `content_part.added`/`output_item.added`——后者较噪声且易与多段交错。**累积口径**：正文 text 从 `output_text.delta` 累积（spec 明确要求"正文=全部 delta 依序拼接"）；reasoning 与 function_call 则以 `output_item.done` 的完整 item 为权威源（reasoning 取 `item.Content`、工具取完整 `Arguments`），既避免自行拼接出错，又与批式 `Generate`（读 `resp.Output`）同源，从而保证"最终消息与批式等价"。reasoning 的 summary delta 只用于 emit 显示，不进入 `msg.ReasoningContent`（批式同样不读 `Summary`）。

### 决策 6：工具调用不流式

参数 delta 不累积、直接忽略；`OutputItemDone` 携带完整调用（`CallID`/`Name`/`Arguments`），据此 emit 单个 `ChunkToolCall`。理由：SSE 前端只关心"调用了哪个工具"，逐字打参数 JSON 无意义且更复杂；且工具在生成结束后才执行，中途的不完整 JSON 不可用。

## Risks / Trade-offs

- **[无消费者，契约可能猜偏]** 本次没有真实 SSE 消费者，`StreamChunk` 形状对着业界标准设计以降低风险 → Mitigation：SDK→累积→返回消息的管道与消费者无关，先落地安全；`StreamChunk` 保持最小，未来 handler 落地时按需增量扩展（加事件类型向后兼容）。
- **[装饰器断链]** `MonitoredProvider` 包裹后，类型断言到 `StreamProvider` 会失效（它未实现 `GenerateStream`）→ Mitigation：本次无消费者故不触发；已在 proposal Impact 记为已知后续，未来接入时补转发并定义流式 token 记账口径。
- **[累积与批式不一致]** 手工累积 reasoning/工具调用若与批式 `Generate` 解析口径不同，会导致等价性破坏 → Mitigation：以 `OutputItemDone` 的完整 item 为权威源（与批式读 `resp.Output` 同源），spec 已将"等价性"列为可测需求。
- **[取消时的资源泄漏]** ctx 取消后未关闭底层流 → Mitigation：`defer stream.Close()`，取消场景返回反映 ctx 的错误。
- **[多段 reasoning/正文交错]** 一次响应理论上可含多段内容 → Mitigation：三段式 + 每段独立 start/end 标记天然支持多段；当前实现按单段正文 + 单段 reasoning 覆盖主路径，多段以标记位隔离。

## Migration Plan

纯增量、无破坏：新增接口与类型 + `OpenApiProvider` 新增一个方法，不改任何现有签名或行为。部署即生效（无人调用则零影响）；回滚 = 删除新增代码，批式链路不受影响。无需数据迁移。

## Open Questions

- 未来 SSE 帧采用命名事件（`event: text-delta`）还是单 data 行判别 JSON（`data:{"type":"text-delta"}`）？——属第②层 handler 决策，不影响本 change 的 `StreamChunk` 契约，可后续再定。

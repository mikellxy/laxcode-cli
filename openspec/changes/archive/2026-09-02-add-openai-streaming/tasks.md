## 1. 契约定义（internal/provider）

- [x] 1.1 在 `interface.go` 新增 `ChunkKind` 及常量：`ChunkTextStart/Delta/End`、`ChunkReasoningStart/Delta/End`、`ChunkToolCall`（start/delta/end 三段式，见 design 决策 2）
- [x] 1.2 在 `interface.go` 新增 `StreamChunk` 结构（`Kind` + `Delta string` + `ToolCall *schema.ToolCall`），事件与 HTTP/SSE 无关
- [x] 1.3 在 `interface.go` 新增可选接口 `StreamProvider`（内嵌 `Provider` + `GenerateStream(ctx, msgs, tools, emit func(StreamChunk)) (*schema.Message, error)`），主接口 `Provider` 保持不变
- [x] 1.4 `go build ./internal/provider/...` 通过（仅类型/接口定义，无消费者）

## 2. OpenApiProvider.GenerateStream 实现（internal/provider/openai.go）

- [x] 2.1 行为不变地抽出一个私有 helper（如 `buildResponseParams(msgs, toolsDefs) responses.ResponseNewParams`），把现有 `Generate` 里的 input/tools 构造逻辑收敛进去，供批式与流式共用，避免重复
- [x] 2.2 实现 `GenerateStream`：调 `p.client.Responses.NewStreaming(ctx, params)`，`defer stream.Close()`，`for stream.Next()` 遍历 `stream.Current()` 的 `Type` 分派
- [x] 2.3 文本事件：首个 `ResponseTextDeltaEvent` 前惰性 `emit(ChunkTextStart)`（bool 标记），累积 `msg.Content += Delta` 并 `emit(ChunkTextDelta{Delta})`；文本 done 时 `emit(ChunkTextEnd)`
- [x] 2.4 reasoning 事件：同三段式累积 `msg.ReasoningContent`；reasoning 项 `OutputItemDone` 取 `msg.ReasoningID` 并 `emit(ChunkReasoningEnd)`
- [x] 2.5 工具调用：`ResponseFunctionCallArgumentsDeltaEvent` 按 `ItemID` 内部暂存参数、不 emit；function_call 项 `OutputItemDone` 取完整 `CallID/Name/Arguments` → append `msg.ToolCalls` 且 `emit(ChunkToolCall{&tc})`（见 design 决策 5/6）
- [x] 2.6 完成事件：`ResponseCompletedEvent.Response.Usage` → 填充 `msg.TokenUsed`（输入/输出）
- [x] 2.7 循环后检查 `stream.Err()`，非 nil 返回错误；确保 ctx 取消能中止遍历并经 `defer Close()` 释放流资源
- [x] 2.8 返回聚合完成的 `*schema.Message`（Role=assistant），其字段口径与批式 `Generate` 一致

## 3. 测试（internal/provider/openai_test.go 或 provider_test.go）

- [x] 3.1 用 `httptest` 起一个返回 canned SSE 事件序列的假服务，`baseURL` 指向它，驱动 `GenerateStream`
- [x] 3.2 断言 emit 顺序符合三段式：`ChunkTextStart → ChunkTextDelta* → ChunkTextEnd`，reasoning 同理且 `Kind` 与正文区分
- [x] 3.3 断言累积等价性：返回 `msg.Content` 等于全部文本 delta 依序拼接；`msg.ToolCalls` 等于全部 `ChunkToolCall` 事件；`msg.TokenUsed` 取自 completed 事件
- [x] 3.4 断言工具调用不以参数片段形式 emit（无半成品 JSON 事件）
- [x] 3.5 能力探测测试：`*OpenApiProvider` 满足 `StreamProvider`，`*AnthropicProvider` 不满足
- [x] 3.6 取消/错误传播测试：可取消 ctx 或让假服务中途返回错误流，断言停止 emit 且返回非 nil 错误（若 SDK 层难以稳定 mock，记为受限并说明验证方式）

## 4. 校验与收尾

- [x] 4.1 `go build ./...` 与 `go vet ./...` 通过
- [x] 4.2 确认改动范围仅限 `internal/provider`（含测试）：terminal / one-shot / subagent / printer / engine.Run / MonitoredProvider 零改动
- [x] 4.3 `openspec validate --change add-openai-streaming` 通过

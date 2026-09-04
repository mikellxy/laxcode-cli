## Context

动机见 proposal.md - Why；行为契约见 specs/（`engine/run-sse`、`engine/sse-http-server` 两个新能力，`engine/one-shot`、`engine/session` 两处修改）。本文件只记塑形实现的技术决策与取舍。

现状约束（来自既有代码与 `2026-09-02-add-openai-streaming` 归档）：

- `engine.Run` 是共享的批式 ReAct 循环，被 `TerminalLoop` / `OneShotLoop` / `SubAgent.Execute` 三前端复用，输出全经注入的 `Printer`（`PrintLLM(msg)` 整条打印）。归档 design 决策 4 已定向：SSE 属「同算法 + 不同输出形状」，应新写兄弟函数 `RunSse`、复用既有原语，而非改造 `Run`。
- provider 第①层已就绪：`provider.StreamProvider.GenerateStream(ctx, msgs, tools, emit func(StreamChunk))` 累积出与批式等价的完整消息，同时经 `emit` 实时吐 `StreamChunk`（text/reasoning 三段式、完整 tool-call）。`emit` 是**同步回调**，运行在调用方 goroutine 上（归档决策 1 选回调而非 channel 的理由）。
- 归档 design 决策 3 划了两层协议红线：`StreamChunk` 只描述一轮生成，loop 级事件（run/step/tool-result）属 engine 第②层，不得下沉 provider。
- `MonitoredProvider` 装饰器包裹真实 provider 做计时 + `sess.RecordGenerate`，但它不转发 `GenerateStream`——`f.Provider` 的运行时类型是 `*MonitoredProvider`，断言到 `StreamProvider` 会失败（归档 proposal 已记为「已知后续：装饰器断链」）。
- `Session` / `SessionDB` 注释明写非并发安全，`GetSession` 读写进程级全局 map；Go 的并发 map 读写是 fatal panic（进程崩溃），非静默 race。
- `Registry.Execute` 内部调 `d.printer.PrintToolCall(...)`；registry 的 printer 经构造注入，nil 时取 `printer.Default()`。one-shot 用 `printer.SetDefault(DiscardPrinter{})` 在装配前统一静默。

## Goals / Non-Goals

**Goals:**

- 以最小侵入兑现第②层：新增 `RunSse` + engine 级 `RunEvent` 词汇 + HTTP SSE handler，`Run` 与三前端零行为变更（除 `too_many_turns` 契约移除）。
- 用一次 spec 中立的内部重构（移除 `MonitoredProvider`、记账内联）解开 SSE 的断链阻塞，并落实 DDD 分层意图。
- handler 退化为薄序列化层：`RunEvent` → `data:` JSON 帧，不含业务逻辑。

**Non-Goals:**

- 不实现同一 `session_id` 的并发轮次安全（本版由客户端串行化，`SessionDB` 锁只保证跨会话不崩溃）。
- 不实现 per-session tracing 落盘（本版 server 级单 tracer 复用）。
- 不做 SSE 端点鉴权 / 限流 / 多 workDir / 请求级 workDir 覆盖。
- 不改 provider 第①层（`StreamChunk` / `GenerateStream` 已冻结）。

## Decisions

### 决策 1：移除 `MonitoredProvider`，记账内联进循环（DDD 分层）

删除装饰器，`Run` 与 `RunSse` 在每次成功生成后、`sess.Append(*msg)` 前，直接 `sess.RecordGenerate(RoundStat{TimeUsed, TokenInput, TokenOutput})`（用量取自返回消息，耗时由循环自身计时）。`main.assembleEngine` 与 `subagent.Execute` 改注入裸 `provider.NewOpenApiProvider(...)`。

- **为何**：装饰器让 `f.Provider` 的运行时类型不再是 `*OpenApiProvider`，`RunSse` 无法断言到 `StreamProvider`——这是 SSE 的直接阻塞点。内联后 `f.Provider` 即裸 provider，断言天然成立。分层上：Engine（持有 provider/tools）与 Session（历史 + 记账）是两个 domain，ReAct loop 是 application 层显式编排二者，不再靠装饰器隐式耦合记账。
- **为何不给 `MonitoredProvider` 补 `GenerateStream` 转发**：那需在装饰器里断言 inner 是否流式、非流式时还要造降级路径，把「是否流式」的复杂度沉进装饰器；且与用户要的去装饰器方向相悖。内联把记账放回循环，`Generate`/`GenerateStream` 两条路径共用同一记账点，更简单。
- **spec 影响**：无。token-usage spec 是机制无关的（只约束「累计==消息用量之和」的不变式与记账时机），换调用方不改可观测行为。
- **顺序不变式**：记账仍在 assistant 消息 Append 之前（沿用 session.go 既有注释口径），崩溃落在中间时 meta 至多超前 history 一条，下次加载以 history 重放自愈。

### 决策 2：`RunSse` 是 `Run` 的兄弟，复用原语、不碰 Printer

```
RunSse(ctx, emit func(RunEvent)) (string, error)
  emit(run-start{session_id})
  for turn:
    emit(step-start{turn})
    msgs,_ = SimpleCompactor.Compress(...)        // 复用
    sp,ok = f.Provider.(StreamProvider)
    if ok:  msg = sp.GenerateStream(genCtx, msgs, tools, func(c StreamChunk){ emit(translate(c)) })
    else:   msg = f.Provider.Generate(genCtx, msgs, tools); emitTextBlock(msg.Content)   // 决策 6 降级
    on err: emit(run-error); return "", err
    sess.RecordGenerate(...); sess.Append(*msg)     // 决策 1 内联记账
    results = f.executeToolCalls(turnCtx, msg.ToolCalls)   // 复用
    for r: emit(tool-result{...}); sess.Append(toolResultMsg)  // buildToolResultContent 复用
    if len(ToolCalls)==0: emit(run-finish{result,token}); return msg.Content, nil
```

- **复用**：`executeToolCalls` / `buildToolResultContent` / `SimpleCompactor.Compress` / `sess.Append` / `sess.RecordGenerate` / tracing helpers 全部照搬，`RunSse` 与 `Run` 只差「生成调用 + 输出形状」这一层，把两条 loop 的漂移面压到最小（等价性是 spec 明列的可测需求）。
- **不碰 Printer**：`RunSse` 不调 `PrintLLM`/`PrintCompressResult`；面向客户端的一切走 `emit`。server 启动 `SetDefault(DiscardPrinter{})`，registry 的 `PrintToolCall` 随之落空（决策 5）。
- **同 goroutine**：`GenerateStream` 的 `emit` 同步回调 → 翻译闭包 → handler 的 sink（写 `ResponseWriter` + `Flush`），全在请求 goroutine 上顺序执行，无需 channel/goroutine 编排（承袭归档决策 1）。

### 决策 3：engine 级 `RunEvent` 承载第②层，handler 只做序列化

`RunEvent` 是扁平结构 + `Type string` 判别式 + omitempty 载荷字段（`Delta`、`ToolCall`、`ToolResult` 相关、`SessionID`、`Turn`、`Result`、`TokenUsed`、`WindowToken`、`Error` 等），形态对齐 provider 的扁平 `StreamChunk`。`RunSse` 内的 `translate(StreamChunk) RunEvent` 把第①层映射到第②层词汇；loop 级事件由 `RunSse` 直接构造。

- **为何扁平 struct 而非 interface 和类型**：JSON 序列化最直接（`data:{"type":"text-delta","delta":"..."}`），与归档把 `StreamChunk` 设计成扁平结构的取向一致；类型安全的 sum type 在跨 HTTP 边界序列化时收益低、样板高。
- **为何不让 `RunSse` 直接写 SSE 帧**：会把 engine 耦合到 `net/http`，`RunSse` 无法脱离 httptest 单测，也违反两层红线。`RunEvent` 传输无关，handler 是薄序列化层（`json.Marshal` → `data: ` 前缀 + `\n\n` + `Flush`）。

### 决策 4：SSE 线格式——单行 `data:` JSON + `type` 判别式（AG-UI/Vercel 对齐）

每个 `RunEvent` 序列化为恰好一行 `data: {json}\n\n` 并 flush；`type` 用 kebab-case（`run-start`/`step-start`/`text-start`/`text-delta`/`text-end`/`reasoning-*`/`tool-call`/`tool-result`/`run-finish`/`run-error`）。终止信号是 `run-finish`/`run-error`，不发 `[DONE]`。

- **为何单 data 行判别 JSON 而非命名事件（`event: text-delta`）**：归档已把 `StreamChunk` 对齐 Vercel AI SDK / AG-UI 的 type-判别 JSON 惯例，单 data 行让 handler 近乎透传、客户端只需一个 JSON parser + `switch(type)`，接现成前端生态省事。
- **为何无 `[DONE]`**：那是 OpenAI Chat Completions 的约定；本设计 AG-UI 对齐，`run-finish`/`run-error` 即显式终止，再叠 `[DONE]` 是冗余且混淆两种协议。

### 决策 5：静默靠 `SetDefault(DiscardPrinter{})`，复用 one-shot 闸门

server 启动时（装配任何引擎前）`printer.SetDefault(printer.DiscardPrinter{})`，随后每请求的 `assembleEngine` 里 `NewDefaultRegistry(nil,...)` 与 `NewAgentEngine` 经 `Default()` 自动继承 Discard。

- **为何复用 SetDefault 而非逐处显式传 Discard**：`assembleEngine` 现成、签名不收 printer；one-shot 已用同一手法（`SetDefault(DiscardPrinter{})`），SSE 沿用即零改 `assembleEngine` 签名，且保证 registry 的 `PrintToolCall` 与散点 `printer.Warnf/Errorf` 一并静默。

### 决策 6：provider 非流式 → 批式降级为单段正文

`RunSse` 断言 `f.Provider.(provider.StreamProvider)`；断言失败（如未来接 `AnthropicProvider`）则调批式 `Generate`，把整条 `msg.Content` 作为 `text-start → text-delta(整段) → text-end` 推送，loop 级事件与工具执行不变。

- **为何降级而非报错**：承袭归档「不实现 `StreamProvider` 即天然降级到批式」的哲学；SSE 客户端仍能拿到完整回答，只是失去逐字增量，不因 provider 选型而整轮失败。

### 决策 7：每请求 `GetSession` + `assembleEngine`，不预注入会话

handler 从 POST body 取 `session_id`（空则毫秒时间串生成）与 `prompt`（空则 4xx 拒绝），`sess := GetSession(workDir, sessionID, planMode)`，`eng := assembleEngine(workDir, sessionID, planMode, tracer)`，`defer closeToolRegistry(eng)`，`sess.Append(user)`，`eng.RunSse(r.Context(), sink)`。

- **为何复用 `assembleEngine`**：它已收口 session 目录创建、工具注册（含 subagent）、`GetSession`、引擎构造；去掉 `MonitoredProvider`（决策 1）后正好产出裸 provider 引擎，per-request 直接复用，与 `SubAgent.Execute` 的「按需装配 + defer Close」同构。
- **workDir 固定**：server 启动解析一次（缺省 `os.Getwd()`），全请求共用，请求不可覆盖（spec 明列）。
- **registry 生命周期**：per-request 装配 → per-request `closeToolRegistry`（bash 后台进程/临时文件随请求回收）；server 无长驻 registry，优雅关停只需停监听 + 等在途请求各自收尾。

### 决策 8：`SessionDB` 读写锁 + 双检惰性加载（并发地板）

给 `SessionDB` 加 `sync.RWMutex` 守护 `sessions` map。`GetSession` 改为：`RLock` 查命中即返回；未命中则 `RUnlock` 后在**锁外**做 `loadSession`（文件 I/O 不阻塞其他请求），再 `Lock` + **二次校验**（若期间他人已插入则丢弃本地加载、返回既有实例）后写入。

- **为何 RWMutex 而非 single-flight 全局粗锁**：粗锁会把不同 session 的请求也串行化；RWMutex 只保护 map，允许不同 `session_id` 真并发，仅把「同 id 并发驱动」这一已知不支持项留给客户端串行化（决策 2 defer）。
- **为何 load 放锁外 + 二次校验**：持写锁做文件 I/O 会阻塞全部 `GetSession`；不放锁外则并发未命中同一 id 会重复 load 并相互覆盖。双检模式两者兼顾，保证「同 id 只保留一个实例」（spec 明列）。
- **残留风险显式化**：锁只解决 map 崩溃；两个请求同时驱动同一 `*Session`（`Append`/`RecordGenerate`/`Messages`）仍 race——这是决策 2 明确 defer 的细粒度并发，由客户端按 `session_id` 串行化规避，spec 与 proposal 均记为已知后续。

### 决策 9：错误双通道——预检 4xx，运行中 `run-error`

SSE 头一旦以 200 flush，HTTP status 不可再改。故：运行开始前的预检失败（非 POST、缺 `prompt`、body 解析失败）走常规 HTTP 4xx + JSON body；流已开始后的失败（生成错误、工具致命错误）走 `run-error` 事件（携带机器可读 `type` 与 `message`，taxonomy 复用 one-shot 收敛后的 `generate` 等）。`RunSse` 仍返回 `(string, error)` 供 handler 记日志，但客户端可见通道是 `run-error`。客户端已断连时对 `ResponseWriter` 的写失败静默忽略（`r.Context()` 已取消）。

### 决策 10：清理 turn 上限残留，改两处 live spec，归档不动

删死代码 `errTooManyTurns`（`Run` 自 `bash-tool-bg-process-safety` 起已无上限判断）、`TerminalLoop` 的 `errors.Is(runErr, errTooManyTurns)` 分支（所有 Run 错误一律 fatal 返回）、one-shot 的 `ErrTypeTooManyTurns` 常量与 `oneShotErrType` 映射分支（收敛为恒 `generate`）、`oneshot_test.go` 相关断言、`main.go:77` 注释。spec 侧改 `engine/one-shot`（错误分类）与 `engine/session`（消息追加子句）两处 live 工件。`turnCnt++` 保留（仍喂 `AttrTurnSeq` 观测属性）。

- **为何归档不动**：归档是不可变历史；`bash-tool-bg-process-safety` 归档已权威记录「移除 turn 上限」，去改归档里的 ADDED-delta 会让历史与当时实际交付不符。用户已确认「先不动已归档 spec 工件」。

## Risks / Trade-offs

- **[`RunSse` 与 `Run` 漂移]** 两条兄弟 loop 未来可能各自演化出不一致 → Mitigation：最大化复用原语（决策 2），把「等价性」列为 spec 可测需求并加对照测试。
- **[同 `session_id` 并发 race]** 决策 8 只防 map 崩溃，不防同会话并发驱动 → Mitigation：显式 defer + 文档化「客户端按 session_id 串行化」；SessionDB 锁 + 双检保证不崩溃、不重复实例。
- **[代理缓冲吞掉流式]** 反向代理可能缓冲 SSE 致客户端收不到增量 → Mitigation：每事件 `Flush`，响应头设 `Cache-Control: no-cache`、`X-Accel-Buffering: no`。
- **[长连接资源占用]** per-request registry 若未随断连回收会泄漏后台进程/临时文件 → Mitigation：`r.Context()` 取消传播到 `RunSse`→`GenerateStream`（已 `defer stream.Close()`）与工具执行，`defer closeToolRegistry` 兜底。
- **[写已断连的连接报错]** 客户端断开后继续 emit 会得到写错误 → Mitigation：ctx 取消后静默忽略写错误，不影响服务端。
- **[降级保真度]** 非流式 provider 下整段一次吐出，失去逐字体验 → 可接受的既定降级（决策 6），非缺陷。

## Migration Plan

纯增量 + 两处内部重构，分步可独立回滚，建议实现顺序：

1. **重构 A（spec 中立）**：移除 `MonitoredProvider`，`Run` 内联 `RecordGenerate`；`main`/`subagent` 改注入裸 provider；改 session.go 文档注释；删/改相关测试。跑全量测试确认 terminal/one-shot 记账行为不变。
2. **重构 B（turn 上限清理）**：删 `errTooManyTurns` 及其分支；改 `engine/one-shot`、`engine/session` 两处 live spec delta（本 change 内）。
3. **`SessionDB` 读写锁 + 双检加载**（决策 8）。
4. **`RunSse` + `RunEvent` + `translate`**（决策 2/3/6），先以单测驱动（stub StreamProvider 断言事件序列与等价性）。
5. **HTTP handler + server 装配 + `-sse`/`-addr` 参数 + main 接线**（决策 4/5/7/9），httptest 端到端验证 SSE 帧序列、断连取消、静默、优雅关停。

部署即生效：`-sse` 未置位则进程行为与今日完全一致（除 `too_many_turns` 契约移除，该错误类型本就不可触发）。回滚 = 逐步 revert；SSE 是 opt-in，不影响既有模式。无数据迁移（history.jsonl / meta.json 格式不变）。

## Open Questions

- `run-error` 的 `type` 是否需在 `generate` 之外单列 `cancelled`（客户端断连）与 `internal`（工具致命错误）？——不改变事件结构与 spec 契约形状，可后续按需增量扩展（向后兼容）。
- 是否为多轮场景给 text/reasoning 事件附 `message_id` 以显式关联同一段？——v1 靠事件顺序 + `step-start` 边界已足够，附 id 属向后兼容的增量，可延后。
- 是否附带一个 `GET /healthz` 或根路径信息端点？——spec 未要求，可后续加，不影响本次任务拆分。

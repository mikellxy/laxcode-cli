## 1. 重构 A：移除 MonitoredProvider，记账内联（决策 1，spec 中立，SSE 前置）

- [ ] 1.1 `internal/engine/engine.go` `Run`：`Generate` 成功后由循环自身计时并调 `sess.RecordGenerate(RoundStat{TimeUsed, TokenInput, TokenOutput})`（用量取自 `msg.TokenUsed`），位置在 `sess.Append(*msg)` 之前，保持既有「记账先于 Append」不变式
- [ ] 1.2 删除 `internal/engine/monitored_provider.go` 与 `internal/engine/monitored_provider_test.go`
- [ ] 1.3 `cmd/main/main.go` `assembleEngine`：`engine.NewMonitoredProvider(provider.NewOpenApiProvider(...), sess)` → 裸 `provider.NewOpenApiProvider(provider.Info{})`
- [ ] 1.4 `internal/engine/subagent.go` `Execute`：同 1.3，装配改用裸 provider
- [ ] 1.5 `internal/engine/session.go`：更新引用 MonitoredProvider 的文档注释（`TokenUsed`/`Rounds`/`RecordGenerate`/`Append` 处「由 MonitoredProvider 上报」→「由 ReAct loop 上报」）
- [ ] 1.6 `internal/engine/tracing_test.go`：装配不再经 `NewMonitoredProvider` 包装（改传裸 stub provider），保持 span 树与 token 属性断言通过
- [ ] 1.7 `go build ./... && go test ./internal/engine/...` 通过；`session_test.go` / `oneshot_test.go` 中直接调 `RecordGenerate` 的用例仍绿，确认 terminal/one-shot 记账行为不变

## 2. 重构 B：清理 turn 上限残留（决策 10）

- [ ] 2.1 `internal/engine/engine.go`：删 `var errTooManyTurns` 及其注释；简化 `TerminalLoop` 错误分支——移除 `errors.Is(runErr, errTooManyTurns)` 的 warn-continue 分支，所有 `Run` 错误一律 `return fmt.Errorf(...)`；保留 `turnCnt++`（仍喂 `AttrTurnSeq`）
- [ ] 2.2 `internal/engine/oneshot.go`：删 `ErrTypeTooManyTurns` 常量；`oneShotErrType` 收敛为恒返回 `ErrTypeGenerate`（或移除该 helper 直接内联）
- [ ] 2.3 `internal/engine/oneshot_test.go`：删除 `oneShotErrType(errTooManyTurns)` / 包装映射相关断言
- [ ] 2.4 `cmd/main/main.go`：更新第 77 行注释，去掉 `too_many_turns` 提及（exit code 说明仅余 generate/usage）
- [ ] 2.5 核对 live spec delta 已覆盖 turn 上限描述：`specs/engine/one-shot/spec.md`（错误分类）与 `specs/engine/session/spec.md`（消息追加子句）——本 change 已写，此步为核对归档工件未被改动
- [ ] 2.6 `go build ./... && go test ./...` 通过

## 3. SessionDB 并发地板（决策 8）

- [ ] 3.1 `internal/engine/session.go`：`SessionDB` 结构加 `sync.RWMutex` 字段守护 `sessions` map
- [ ] 3.2 `GetSession` 改双检惰性加载：`RLock` 命中即返回；未命中 `RUnlock` 后在锁外做 `loadSession` + `BuildSysPrompt` + `View`，再 `Lock` 二次校验（期间他人已插入则丢弃本地加载、返回既有实例）后写入 map
- [ ] 3.3 更新 `Session` / `SessionDB` 并发注释：map 已由读写锁保护（跨会话并发安全）；同一 session 的并发轮次驱动仍不支持，由客户端按 `session_id` 串行化（细粒度并发留待未来）
- [ ] 3.4 单测（`go test -race`）：并发访问不同 `session_id` 不崩溃且各自得到正确对象；并发访问同一未缓存 `session_id` 最终只保留一个实例

## 4. RunSse + RunEvent（engine 第②层，决策 2/3/6）

- [ ] 4.1 新增 `internal/engine/runsse.go`：定义扁平结构 `RunEvent`（`Type string` + omitempty 载荷字段：`Delta`、`ToolCall`、`ToolCallID`/`Name`/`Output`/`IsError`、`SessionID`、`Turn`、`Result`、`TokenUsed`、`WindowToken`、`ErrorType`/`Message`）
- [ ] 4.2 定义事件 `type` 常量：loop 级 `run-start`/`step-start`/`tool-result`/`run-finish`/`run-error`；第①层透传 `text-start`/`text-delta`/`text-end`/`reasoning-start`/`reasoning-delta`/`reasoning-end`/`tool-call`（kebab-case）
- [ ] 4.3 实现 `translate(provider.StreamChunk) RunEvent`：把 `ChunkText*`/`ChunkReasoning*`/`ChunkToolCall` 一一映射到对应 `RunEvent`
- [ ] 4.4 实现批式降级 helper：把整条正文作为 `text-start → text-delta(整段) → text-end` 三事件推送
- [ ] 4.5 实现 `RunSse(ctx, emit func(RunEvent)) (string, error)`：`emit(run-start)`；循环内 `emit(step-start)` → `SimpleCompactor.Compress` → 断言 `f.Provider.(provider.StreamProvider)`，成立走 `GenerateStream(genCtx, msgs, tools, func(c){emit(translate(c))})`，否则批式 `Generate` + 4.4 降级 → 记账内联（成功生成后、`sess.Append` 前）→ `executeToolCalls` → 每结果 `emit(tool-result)` + `sess.Append` → 无工具调用则 `emit(run-finish{result,token_used,window_token})` 并返回；复用 `Run` 的 tracing span 结构；全程不调 Printer
- [ ] 4.6 取消与错误：`ctx` 取消中止当前生成与后续轮次并释放资源；生成/工具错误 `emit(run-error{message,error_type})` 且返回非 nil 错误，错误后不再 emit 增量
- [ ] 4.7 单测（stub `StreamProvider` 发 canned `StreamChunk` 序列）：断言事件序列（`run-start→step-start→reasoning/text 三段式→tool-call→tool-result→run-finish`）、最终文本与 token 记账与批式 `Run` 等价、非流式 provider 降级路径、ctx 取消路径

## 5. HTTP SSE server + main 接线（决策 4/5/7/9）

- [ ] 5.1 新增 `internal/engine/sse_server.go`：`RunSseServer(ctx, addr string, newEngine func(sessionID string) (*AgentEngine, error)) error`——注入式引擎工厂（engine 不 import main，由 main 传闭包复用 `assembleEngine`）
- [ ] 5.2 POST handler：方法非 POST → 405；解析 body `{session_id, prompt}`；`prompt` 空 → 400 + JSON；`session_id` 空 → 生成毫秒时间串；`eng := newEngine(sessionID)`、`defer closeToolRegistry(eng)`、`eng.Session.Append(user 消息)`（预检失败走 HTTP 4xx，非 SSE）
- [ ] 5.3 SSE 响应头（`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`、`X-Accel-Buffering: no`）+ 取 `http.Flusher`；sink = `func(ev RunEvent){ json.Marshal → 写 "data: "+json+"\n\n" → Flush }`，`ctx` 取消后的写错误静默忽略
- [ ] 5.4 调 `eng.RunSse(r.Context(), sink)`；以 `run-finish`/`run-error` 为终止信号，不追加 `[DONE]`；`RunSse` 返回的 error 仅记日志
- [ ] 5.5 `RunSseServer` 装配 `http.Server{Addr, Handler}` + 优雅关停（监听终止信号 → `server.Shutdown(ctx)`，停收新请求、等在途请求各自 `closeToolRegistry` 收尾）
- [ ] 5.6 `cmd/main/main.go`：新增 `-sse`（开关）与 `-addr`（监听地址）参数，仅命令行来源；`-sse` 置位分支——`printer.SetDefault(printer.DiscardPrinter{})` 静默闸门、`workDir` 解析一次（缺省 `os.Getwd()`）、装配 server 级单一 `traceHandle`、构造 `newEngine` 闭包（捕获 workDir/planMode/tracer 复用 `assembleEngine`）、调 `engine.RunSseServer(...)`，不进 REPL/one-shot
- [ ] 5.7 httptest 端到端：合法 POST 帧序列逐帧 flush 且以 `run-finish` 收尾；缺 `prompt` → 400；非 POST → 405；客户端断连 → 服务端经 `r.Context()` 取消中止且资源回收；stdout/stderr 无中间过程输出
- [ ] 5.8 `go test -race`：并发不同 `session_id` 的多个请求不崩溃、各自独立完成

## 6. 校验与收尾

- [ ] 6.1 `go build ./... && go vet ./... && go test ./...`（含 `-race`）全通过
- [ ] 6.2 核对改动范围符合 proposal Impact：`internal/provider` 零改动；terminal/one-shot 除 `too_many_turns` 契约移除外行为不变；`Printer` 接口不变
- [ ] 6.3 `openspec validate --change add-sse-streaming-mode` 通过
- [ ] 6.4 `README.md`：增补 SSE 模式说明（第三交互模式、`-sse`/`-addr`、POST `{session_id, prompt}` 契约、`data:{"type":...}` 事件类型表、同会话并发需客户端串行化的限制）

## 1. Session 结构体与持久化（internal/engine/session.go）

- [x] 1.1 定义 `Session` 结构体：session id、非导出的 `messages []schema.Message`、`history.jsonl` 文件路径；提供构造入口（新建空 session / 从文件加载）
- [x] 1.2 实现 load 流程：读取指定 session 的 `.laxcode/.session/<id>/history.jsonl`，逐行 `json.Unmarshal` 为消息并按行序入历史；坏行与空白行跳过并输出警告（沿用 `[LaxCode]` 黄色警告惯例），文件不存在视为空历史
- [x] 1.3 实现 `Append(msg)`：内存追加 + 以追加模式（O_APPEND）向 `history.jsonl` 写入该消息的一行 JSON；首次写入前按需创建 `.laxcode/.session/<id>/` 目录（空会话不产生目录）；写盘失败输出显眼警告并返回继续运行语义，不 panic
- [x] 1.4 实现 `View(sysPrompt)`：返回 `[system消息] + 当前历史` 的新拼切片，供 provider 消费；不修改内部状态

## 2. SessionDB 与全局初始化（internal/engine/session.go）

- [x] 2.1 定义 `SessionDB` 结构体（session id → `*Session` 映射）与非导出包级全局变量 `sessionDB`
- [x] 2.2 实现 `InitSessionDB(workDir, sessionID)`：创建 db、仅加载该指定 id 的一个 session（存在则 load，不存在则新建空 session 入映射）、挂到全局；不扫描其他 session
- [x] 2.3 实现包内查询函数：按 session id 从全局 db 返回 `*Session`，供 Loop 使用；全局变量不导出

## 3. 主循环改造（internal/engine/main_loop.go）

- [x] 3.1 移除 `AgentEngine.contextHis` 字段与 `Run` 中"复制 slice 头 + defer 回写"逻辑
- [x] 3.2 `Loop` 签名改为 `Loop(ctx, sessionID)`：启动时从全局 db 查询 session、用 `BuildSysPrompt` 构建 system prompt（每次重建、不落盘）并持有；REPL 中用户输入通过 `session.Append` 记录
- [x] 3.3 `Run` 签名改为 `Run(ctx, sess)`：工具循环内每次 `Generate` 前调 `View(sys)` 组装历史；assistant 回复（含工具调用）与工具结果均通过 `session.Append` 记录；errTooManyTurns 语义不变（中断时已产生消息自动留痕）

## 4. 入口改造（cmd/main/main.go）

- [x] 4.1 解析命令行参数指定 session id；未指定时以 `20060102-150405.000` 格式（毫秒精度）生成当前时间串 id
- [x] 4.2 调用 `engine.InitSessionDB(env.WorkDir, sessionID)` 后以 `Loop(ctx, sessionID)` 启动

## 5. 测试

- [x] 5.1 新增 session 单测（临时目录、纯函数路径，不碰全局）：load/append round-trip（写入若干消息后重新加载，历史一致且顺序不变）、坏行与空行跳过、不存在的 id 新建空 session、`View` 头部为 system 且不影响后续追加
- [x] 5.2 适配 `engine_test.go`：不再直接操作 `contextHis`，改为构造 session 并经 Append 注入 system prompt 与用户问题
- [x] 5.3 `go build ./...`、`go vet ./...`、`go test ./...` 全部通过

## 6. 端到端验证

- [x] 6.1 手工验证：运行一轮含工具调用的对话，检查 `history.jsonl` 逐行生成、只含 user/assistant/tool 消息（无 system）；重启并以相同 session id 启动，历史恢复后续聊正常

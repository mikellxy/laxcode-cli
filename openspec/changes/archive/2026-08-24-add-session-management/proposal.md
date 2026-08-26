## Why

AgentEngine 的对话历史目前只存在于内存字段 `contextHis` 中，进程退出即全部丢失，无法中断后继续之前的对话。引入 session 持久化后，对话历史按 session 落盘为 `.laxcode/.session/<session_id>/history.jsonl`，为后续续聊、会话管理等能力打下地基。

## What Changes

- 新增 `internal/engine/session.go`：
  - `Session` 结构体：持有 `[]schema.Message` 对话历史，提供 `Append`（内存追加 + 追加一行 JSON 落盘）与 `View`（组装 `[system] + Messages` 新切片供 provider 消费）
  - `SessionDB` 结构体：session id 到 session 对象的映射，以包级全局变量形式存在（非导出 + Init 函数收口）
  - load 流程：读取指定 session 的 `history.jsonl`，逐行 JSON 反序列化为历史消息；坏行/空行跳过并警告，不阻断启动
- 改造 `../../../../internal/engine/engine.go`：
  - 移除 `AgentEngine.contextHis` 字段及其 defer 回写逻辑，历史唯一真相源变为 Session
  - `Loop` 签名改为接收 `session_id`，从全局 SessionDB 查询 session；system prompt 仍在启动时构建（不落盘、每次重建）
  - `Run` 接收 session 对象，本轮产生的用户输入、assistant 回复、工具结果均通过 `Session.Append` 记录到 history.jsonl
- 改造 `cmd/main/main.go`：
  - 增加命令行参数指定 session id；未指定时使用当前时间串（含毫秒，防同秒冲突）生成新 id
  - 启动时初始化全局 SessionDB，本版只 load 命令行指定的这一个 session（文件不存在则新建空 session）
- 持久化范围约定：history.jsonl 只存 user/assistant/tool 消息，system prompt 不落盘
- 容错约定：历史写盘失败输出显眼警告但 REPL 继续运行，不中断会话

## Capabilities

### New Capabilities
- `engine/session`: session 对话历史的持久化与生命周期管理——SessionDB 的初始化与查询、Session 的加载（jsonl 反序列化）、消息追加落盘、供 provider 消费的历史视图组装

### Modified Capabilities
<!-- 无：现有 specs（context/skill-index、tools/edit-file、tools/write-file）的需求均不受影响 -->

## Impact

- `internal/engine/session.go`：新增（Session / SessionDB / 全局 db 初始化与查询入口）
- `../../../../internal/engine/engine.go`：改造（移除 contextHis、Loop/Run 签名变更、defer 回写逻辑删除）
- `cmd/main/main.go`：改造（session id 命令行参数、SessionDB 初始化）
- `../../../../internal/engine/engine_test.go`：适配（不再直接操作 contextHis，改走 session 构造）
- 存储约定：新增 `.laxcode/.session/<session_id>/history.jsonl`（目录已存在，为空）
- 无外部依赖变化；provider / tools / context 包零改动

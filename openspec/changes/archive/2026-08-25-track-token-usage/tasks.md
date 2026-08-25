# Tasks: track-token-usage

## 1. schema 与 provider 层

- [x] 1.1 `internal/schema/message.go`：为 `TokenUsed` 字段补充注释，说明 raw 计费口径（TokenInput 为本次调用完整输入含 system prompt 与全部历史、仅 assistant 消息携带非零值）
- [x] 1.2 `internal/provider/openai.go`：`Generate` 组装 assistant 消息时从 `resp.Usage` 回填 `TokenUsed.TokenInput`/`TokenOutput`（int64 转 int），并清理文件中已注释掉的旧输出解析代码块

## 2. session 统计维护

- [x] 2.1 `internal/engine/session.go`：为 `Session.TokenUsed`（累计消耗，加和不变式）与 `Session.WindowToken`（窗口占用，最后一条非零 usage 消息原值、含固有滞后说明）补充字段注释
- [x] 2.2 `internal/engine/session.go`：`Append` 中累加 `TokenUsed += msg.TokenUsed`；消息 `TokenUsed` 非零时刷新 `WindowToken = msg.TokenUsed` 并触发 meta.json 重写
- [x] 2.3 `internal/engine/session.go`：新增 `SessionMeta` struct（`version` + `token_used` + `window_token`）及 meta.json 写入函数：同目录 CreateTemp + `os.Rename` 原子整体重写，失败走 `warnHistoryWrite` 同款警告哲学不中断会话，零统计不创建文件

## 3. 加载重放

- [x] 3.1 `internal/engine/session.go`：`loadSession` 循环内单遍累加 `TokenUsed`、记录最后一条非零 usage 消息恢复 `WindowToken`；meta.json 不参与恢复（缺失/损坏静默忽略）

## 4. 验证

- [x] 4.1 单元测试：Append 累加与 WindowToken 刷新（含零用量消息不影响统计、user/tool 消息不触发 meta 写入）
- [x] 4.2 单元测试：loadSession 重放（多条带用量消息求和、取最后非零消息恢复窗口、无 token_used 键的旧行按零值处理、坏 meta.json 不阻断）
- [x] 4.3 集成验证：真实对话数轮后检查 history.jsonl 各行含 token_used、meta.json 两项统计正确、续聊重放后统计与 meta 一致
- [x] 4.4 `go build ./...` 与 `go vet ./...` 通过

# Proposal: track-token-usage

## Why

agent 与大模型的每次交互都在消耗 token，但目前系统对消耗毫无感知：`resp.Usage` 在 provider 层被直接丢弃，用户既不知道一个 session 累计烧了多少 token（成本），也不知道上下文窗口当前占用多大（为未来的上下文压缩决策做铺垫）。`schema.Message.TokenUsed` 与 `Session.TokenUsed`/`WindowToken` 字段已就位，缺的是把它们串起来的记账机制。

## What Changes

- provider 层：`OpenApiProvider.Generate` 把 `resp.Usage` 的 `InputTokens`/`OutputTokens` 以 raw 口径（本次调用的完整计费值，含 system prompt 与全部历史）回填到返回的 assistant 消息的 `TokenUsed` 字段
- session 层：`Session.Append` 在追加消息时累加 `TokenUsed`（累计计费 token），并在追加携带 usage 的 assistant 消息时刷新 `WindowToken`（上下文窗口占用快照，= 最后一条有记录的 assistant 消息的 `TokenUsed` 原样拷贝）
- session 层：新增 `meta.json` 持久化文件（位于 `.laxcode/.session/<session_id>/` 下），存放 `TokenUsed` 与 `WindowToken` 供用户快查；顶层 JSON 对象带 `version` 字段保留扩展性
- session 层：`loadSession` 加载历史时单遍重放求和恢复 `TokenUsed`、取最后一条非零 usage 消息恢复 `WindowToken`；`meta.json` 仅为快查快照，损坏/缺失/滞后不阻断启动（重放值为准）
- user/tool/system 消息的 `TokenUsed` 保持零值不回填；feature 上线前的旧会话历史的花费视为不可知，从 0 开始累计

## Capabilities

### New Capabilities

- `engine/token-usage`: token 消耗记账机制--provider 回填 assistant 消息的 raw usage、session 维护累计消耗与窗口占用两个口径的统计、meta.json 快照持久化、加载时重放恢复，以及各环节的容错行为

### Modified Capabilities

（无。`engine/session` 现有需求--history.jsonl 格式、加载容错、视图组装等--行为均不变，仅同目录新增文件。）

## Impact

- `internal/schema/message.go`：`TokenUsed` 字段已存在，无需改动（序列化行为已满足：无 omitempty，每行 JSON 恒输出）
- `internal/provider/openai.go`：`Generate` 组装 assistant 消息时回填 `TokenUsed`
- `internal/engine/session.go`：`Session` 结构（`TokenUsed`/`WindowToken` 字段已存在）、`Append` 累加与刷新逻辑、`loadSession` 重放逻辑、meta.json 读写
- 磁盘布局：每个 session 目录新增 `meta.json`（重写式更新，tmp+rename 原子替换）
- 不涉及 API/依赖变更；engine 主循环与 tools 层零改动

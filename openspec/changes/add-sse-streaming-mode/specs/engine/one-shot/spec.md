## ADDED Requirements

### Requirement: 失败错误分类与 exit code

one-shot 模式失败时，`error` 字段 SHALL 携带机器可读的 `type` 与 `message`，并以非零 exit code 退出。`type` SHALL 至少区分：`usage`（参数用法错误，此时进程尚未建立 session，`session_id` 为空、token 统计为零值）、`generate`（模型调用失败）。

<!-- 替代被移除的「错误语义与 exit code」：turn 上限已废，too_many_turns 终态与「工具循环超限」Scenario 一并删除，错误分类收敛为 usage / generate。 -->

#### Scenario: 用法错误
- **WHEN** 参数校验失败（缺 `-workdir` 或无提示词）
- **THEN** stdout 输出 `error.type` 为 `usage` 的 JSON，`session_id` 为空串，exit code 非零

#### Scenario: 模型调用失败
- **WHEN** 任务执行中途模型调用报错
- **THEN** stdout 输出 `error.type` 为 `generate` 的 JSON，已产生的 session 历史与 token 统计保留，exit code 非零

## REMOVED Requirements

### Requirement: 错误语义与 exit code
**Reason**: turn 上限已在 `2026-09-02-bash-tool-bg-process-safety` 移除，`too_many_turns` 自此不可再被触发，其「工具循环超限」Scenario 描述的是已不存在的行为。OpenSpec 不支持在保留需求名的前提下删单个 Scenario，故整条移除并以本 change 中 cleaned 版「失败错误分类与 exit code」替代。
**Migration**: 调用方不再需处理 `error.type == too_many_turns`；one-shot 失败的 `type` 仅剩 `usage`（参数错误）与 `generate`（模型调用失败），exit code 语义不变。参见 ADDED 的「失败错误分类与 exit code」。

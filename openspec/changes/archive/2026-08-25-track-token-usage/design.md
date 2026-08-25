# Design: track-token-usage

## Context

见 proposal.md 的动机。当前 `schema.Message.TokenUsed`、`Session.TokenUsed`、`Session.WindowToken` 字段均已存在但无任何维护逻辑；`OpenApiProvider.Generate` 拿到 `resp.Usage` 后直接丢弃。openai-go v3 Responses API（非流式）的 `resp.Usage` 为值类型且必然存在，`InputTokens`/`OutputTokens` 与 `schema.TokenStatistics` 的 `TokenInput`/`TokenOutput` 一一映射，`TotalTokens` 为派生值可忽略。会话历史持久化为 append-only 的 `history.jsonl`（每行一条消息 JSON），`TokenUsed` 无 omitempty、struct 类型，序列化时每行恒输出（零值也输出）。

## Goals / Non-Goals

**Goals:**

- assistant 消息携带 raw 计费口径的 usage，随 history.jsonl 天然持久化
- `Session.TokenUsed`（累计消耗）与 `WindowToken`（窗口占用）两个统计由唯一写路径维护，加载时单遍重放恢复
- meta.json 快照供用户直接查看，容错宽松

**Non-Goals:**

- 不为 user/tool/system 消息估算或回填 token 用量（无 token 计数端点，本地无 tokenizer，精确拆分不可知）
- 不做成本换算（金额）、不做缓存命中分析（`InputTokensDetails.CachedTokens` 暂不使用）
- 不基于 `WindowToken` 做上下文压缩/截断（仅为将来的该类决策提供数据）
- 不回溯补录记账启用前的旧会话历史花费（信息已丢失）

## Decisions

### D1: usage 口径选 raw（本次调用完整计费值），不选 delta（上下文增量）

assistant 消息的 `TokenInput` = 本次请求全部输入的 token 数（含 system prompt 与当时全部历史），`TokenOutput` = 本次输出。备选的 delta 口径（本次 input 减上次 input）能表达"context 增量"，但需要差值计算与负值钳制，且旧会话语义复杂。raw 口径下：

- provider 零逻辑直接抄 `resp.Usage`，实现最省
- `TokenUsed` 加和 = 累计计费 token（input 随调用次数线性重复计入，这正是真实账单）
- 窗口占用的诉求由 `WindowToken` 单独承担（见 D3），不依赖 delta

### D2: 统计维护收敛在 `Session.Append` 单点

`TokenUsed` 只经 `Append` 累加（`TokenUsed += msg.TokenUsed`），不变式"加和 == 全部消息用量总和"由构造保证；不存在绕过 messages 的更新路径，因此统计始终可从 history.jsonl 重放推导。备选方案是在 engine 主循环里于每次 Generate 后单独更新 Session，被否决：会产生第二个写路径，破坏重放推导的成立条件。engine 层因此零改动。

### D3: `WindowToken` = 最后一条非零 usage 的 assistant 消息的 `TokenUsed` 原样拷贝

`WindowToken.TokenInput + WindowToken.TokenOutput ≈ system prompt + 全部历史`的当前窗口占用。原样拷贝不做算术合并，字段含义为：TokenInput 是"除最后一条 assistant 自身外的全部上下文"，TokenOutput 恰好补上它自己那块（需在字段注释中写明）。刷新时机为 Append 携带非零用量的消息时；其后追加 user/tool 消息导致的滞后是该口径的固有属性（本地无法得知新消息 token 数），接受并在注释中说明。备选"assistant 记录自身大小、user/tool 消息各自记账"被否决：需要 tokenizer 估算加 history.jsonl 尾部重写，成本高且只换来不可知的拆分。

### D4: meta.json 定位为快查快照，重放为权威

`TokenUsed` 与 `WindowToken` 均可从 history.jsonl 单遍重放推导（loadSession 本来就逐行 unmarshal，重放零边际成本），因此 meta.json 中的值是冗余快照，供用户 `cat meta.json` 直接查看（用户明确要求两项统计都存放）。加载策略：以重放结果为准，meta.json 缺失/损坏/滞后一律忽略并容错。这使 meta.json 的容错极简，也规避了双真相源的分歧问题。将来一旦出现不可推导的记账项或元数据（如失败调用开销、created_at、model），meta.json 再升级为权威存储。

格式：

```json
{
  "version": 1,
  "token_used":   { "token_input": N, "token_output": N },
  "window_token": { "token_input": N, "token_output": N }
}
```

顶层对象 + `version` 字段；Go 侧定义 `SessionMeta` struct（`Version int` + 两个 `schema.TokenStatistics`），未知字段靠 unmarshal 默认行为忽略，未来加字段无需迁移。

### D5: meta.json 整体重写 + 原子替换，写入顺序在 history 行之后

统计变化频率极低（仅带用量的 Append 触发，约每次 Generate 一次），整体重写无性能问题。同目录 CreateTemp + `os.Rename` 原子替换，避免崩溃留下半截 JSON。Append 内先写 history 行、再重写 meta.json：崩溃落在中间时 history 多一行而 meta 滞后一条，重放自愈，反向超前不会发生。写失败沿用 `warnHistoryWrite` 同款哲学（显眼警告、不中断会话）。空会话/零统计不创建 meta.json，与"从未 Append 的会话不在磁盘留痕"的现有哲学一致。

### D6: 加载重放的单遍算法

`loadSession` 现有循环内顺手完成：

```
每条消息: TokenUsed.TokenInput/TokenOutput 各自累加
          若 msg.TokenUsed 非零: windowCandidate = msg.TokenUsed
循环结束: WindowToken = windowCandidate（无则保持零值）
```

旧会话（消息无 token_used 键，unmarshal 得零值）自然得出"累计从零、窗口未知"的结果，无需特判。

## Risks / Trade-offs

- [崩溃窗口内 meta.json 与 history 短暂不一致] -> 重放为权威，下次启动自愈；meta 本就是快照
- [`WindowToken` 在 user/tool 消息追加后偏小（下界）] -> 固有滞后，注释说明；下一次调用后自动收敛
- [旧会话续聊时 `WindowToken` 为零值（未知态），下游若据其触发压缩会误判"窗口很空"] -> 当前无下游消费者；将来消费时需把"刚刷新后的首轮"与"未知"区分（可结合消息时间或调用次数判断）
- [raw 口径的 input 随调用次数重复累计，用户可能误读为"context 大小"] -> 两个统计字段分工明确，注释与 meta.json 字段命名（`token_used` vs `window_token`）承载语义
- [流式响应（将来）无聚合 `resp.Usage`] -> 非本次范围；届时在流结束处聚合后走同一回填路径即可，设计不变

## Migration Plan

纯增量变更，无数据迁移。旧 history.jsonl 无 token_used 键的消息 unmarshal 为零值，语义正确（花费不可知、从零累计）。回滚 = 还原代码，meta.json 成为无人读取的多余文件，可手工删除，无兼容性影响。

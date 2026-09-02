## Purpose

为 laxcode 的两种运行模式（交互 / one-shot）提供基于 OpenTelemetry API
的可插拔分布式追踪：默认零开销不产生任何观测输出，用户注入自定义
TracerProvider 实现后即可获得完整的任务-运行-循环-生成-工具 span 树。

## Requirements

### Requirement: 可插拔 Tracer 与 noop 默认

引擎与工具注册表 SHALL 支持以策略模式注入 OpenTelemetry `trace.Tracer`
（结构体字段，构造期传入）；未注入（nil）时 MUST 缺省为 no-op tracer，
此时系统行为与性能开销 SHALL 与未启用追踪一致。产品 SHALL 仅依赖
OpenTelemetry API 模块，SHALL NOT 引入 SDK 或任何 exporter 依赖，
SHALL NOT 提供真实上报后端的实现。

#### Scenario: 默认装配无任何观测输出

- **WHEN** 用户以默认方式装配引擎与工具注册表（不注入 Tracer）
- **THEN** 全部埋点走 no-op 实现，进程不产生任何 trace 输出，
  现有功能行为与性能不变

#### Scenario: 注入自定义 TracerProvider 后埋点生效

- **WHEN** 用户注入由自定义 `trace.TracerProvider` 实现派生的 Tracer
- **THEN** 各埋点产生的 span 经由实现侧的 `Span.End()` 到达用户
  上报逻辑，laxcode 埋点代码无需任何改动

### Requirement: 交互模式终端任务 span

交互模式下，每一次用户输入 SHALL 开启一条新 trace，root span 为
`terminal-task`，记录该次输入任务的整体耗时与 token 合计（input 与
output 分别记录），并携带 `session_id` 与该输入在会话中的序号属性。

#### Scenario: 一轮用户输入生成一条完整 trace

- **WHEN** 用户在交互模式输入一条任务并等待 Agent 完成
- **THEN** 产生一条新 trace，root 为 `terminal-task` span，其下嵌套
  该次任务的 agent 运行子树，root span 上的 token 合计等于该轮输入
  前后 session 累计 token 的差值

#### Scenario: 多次输入产生多条独立 trace

- **WHEN** 用户在同一 session 中连续输入两轮任务
- **THEN** 产生两条相互独立的 trace，各自携带相同的 `session_id` 与
  递增的任务序号

### Requirement: one-shot 模式追踪

one-shot 模式下，整个任务 SHALL 产生一条 trace，root span 为 agent
运行 span，SHALL NOT 存在 `terminal-task` 层。

#### Scenario: one-shot 任务生成以 agent 运行为根的 trace

- **WHEN** 以 one-shot 模式执行一个任务
- **THEN** 产生一条 trace，root 为 agent 运行 span，且树中不存在
  `terminal-task` span

### Requirement: react 循环与生成 span 树

每次 Agent 运行 SHALL 生成一个 `agent-run` span；运行中的每一轮
react 循环 SHALL 生成一个 `react-loop` 子 span（携带循环序号属性）；
每次 LLM 生成调用 SHALL 生成一个 `llm-generate` 子 span，记录本次
调用的耗时、input/output token 用量与工具调用数。token 用量口径
SHALL 与 session 记账口径一致（raw 计费口径）。

#### Scenario: 多轮循环形成正确嵌套

- **WHEN** 一次 Agent 运行经历 3 轮 react 循环后结束
- **THEN** `agent-run` span 下存在 3 个按序号标记的 `react-loop`
  子 span，每个子 span 下各有一个 `llm-generate` span

#### Scenario: 无工具调用的直接回答

- **WHEN** 模型首轮即给出无工具调用的最终回答
- **THEN** `agent-run` 下仅有一个 `react-loop`，其下仅有一个
  `llm-generate` span，其工具调用数属性为 0

### Requirement: 工具执行 span

每次工具执行 SHALL 生成一个 `tool-exec` span，作为所属 `react-loop`
的子 span，携带 `session_id` 与 `tool_name` 属性，记录执行耗时与
错误状态；`tool-exec` span SHALL NOT 记录任何 token 用量。并行执行的
多个工具 SHALL 生成为同一 `react-loop` 下的兄弟 span。

#### Scenario: 工具 span 携带名称与耗时

- **WHEN** Agent 在某轮循环中调用 bash 工具并成功执行
- **THEN** 该轮 `react-loop` 下生成一个 `tool-exec` span，
  `tool_name` 属性为 bash，span 时长等于工具实际执行耗时，且不携带
  任何 token 属性

#### Scenario: 并行工具调用生成兄弟 span

- **WHEN** Agent 在一轮循环中并行调用多个 read_file
- **THEN** 每个 read_file 调用各生成一个 `tool-exec` span，全部挂在
  同一 `react-loop` 下，时间轴允许重叠

### Requirement: 子 Agent 追踪嵌套

子 Agent 的运行 SHALL 与主 Agent 处于同一 trace：子 Agent 的
`agent-run` span 自动嵌套在触发它的 `tool-exec` span 之下，并以
`agent_role` 属性区分 main / sub。

#### Scenario: 子 Agent 子树挂载在工具 span 下

- **WHEN** 主 Agent 调用 sub_agent 工具且子 Agent 内部执行 2 轮循环
- **THEN** 子 Agent 的 `agent-run`（`agent_role=sub`）嵌套在该
  `tool-exec` span 下，子树内部完整包含自己的 `react-loop` /
  `llm-generate` / `tool-exec` 层级

### Requirement: span 关联与标识

所有 span SHALL 携带 `session_id` 业务键，使后端可按会话过滤聚合。
span / trace 的唯一标识 SHALL 由 TracerProvider 实现侧生成（trace_id /
span_id / parent_span_id），产品 SHALL NOT 自定义 ID 生成体系。

#### Scenario: 按 session 过滤 trace

- **WHEN** 用户在支持属性查询的追踪后端按 `session_id` 过滤
- **THEN** 该 session 产生的全部 trace 与 span 均可被检索到

### Requirement: 错误状态记录

LLM 生成失败 SHALL 在对应 `llm-generate` span 上记录错误并置错误
状态；工具执行失败 SHALL 在对应 `tool-exec` span 上记录错误并置错误
状态；react 循环次数超限 SHALL 在 `agent-run` span 上记录为错误终态。

#### Scenario: 工具失败在 span 上可见

- **WHEN** 某次工具执行返回错误
- **THEN** 对应 `tool-exec` span 携带错误信息与错误状态，且上层
  `react-loop` 与 `agent-run` span 不因此被置为错误（Agent 可基于
  错误继续修正）

#### Scenario: 生成失败终止运行

- **WHEN** LLM 生成调用失败导致运行终止
- **THEN** 对应 `llm-generate` span 与 `agent-run` span 均携带错误
  信息与错误状态

### Requirement: 退出生命周期

进程退出前 SHALL 调用追踪 Shutdown 钩子；默认 no-op 实现下该调用为
空操作。注入真实 TracerProvider 实现时，Shutdown SHALL 触发实现侧的
收尾逻辑（如批量导出的强制 flush），且 SHALL 在 one-shot 模式写出
结果、交互模式主循环返回之后、进程退出之前完成。

#### Scenario: one-shot 快速退出不丢 span

- **WHEN** 注入了批量导出实现且 one-shot 任务在短于批量导出间隔的
  时间内完成
- **THEN** 进程退出前 Shutdown 被调用，该任务的全部 span 均被实现侧
  收到，无尾部丢失

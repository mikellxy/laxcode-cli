# Design: add-otel-tracing

## Context

现状与约束（动机见 proposal.md）：

- `context.Context` 已全链路贯通：TerminalLoop → Run → Provider.Generate /
  executeToolCalls → Registry.Execute → tool.Execute，连 read_file
  fork-join 的 goroutine 也携带 ctx——OTel 基于 ctx 的 span 父子传播
  可直接复用，埋点不改任何函数签名。
- `MonitoredProvider` 装饰器已在 Generate 成功后向 session 记账
  （RoundStat：耗时 + token），并落盘 meta.json。这是"本地指标"通路，
  与本次新增的"对外追踪"通路相互独立。
- `SubAgent` 是注册进 Registry 的普通工具，子 Agent 的 Run 发生在
  `tool.Execute` 内部。
- 项目依赖哲学轻量（provider 为手写 HTTP 客户端），且
  `openspec/config.yaml` 约定新功能归入 internal 子包。
- one-shot 模式进程存活时间可能短于 OTel SDK 批量导出间隔（默认约 5s）。

## Goals / Non-Goals

**Goals:**

- 交互 / one-shot 两种模式的 span 树埋点，层级：terminal-task →
  agent-run → react-loop → {llm-generate, tool-exec}。
- 仅依赖 OTel **API 模块**；默认 noop，未注入时零行为变化、零可见开销。
- 用户经 `trace.TracerProvider` 接口注入自定义实现（或装配官方 SDK），
  laxcode 埋点代码随之零改动生效。
- 提供进程退出前的 `Shutdown` 钩子，保证批量导出模式下尾部 span 不丢。

**Non-Goals:**

- 不提供任何真实上报后端实现（OTLP/jaeger/stdout exporter 均不引入）。
- 不引入 Metrics API（Meter/MeterProvider）：token/耗时以 span 属性
  承载，本地聚合指标继续由 session.Rounds 承担，全局聚合留待后续。
- 不引入 Logs / baggage / 跨进程 propagation（laxcode 是单进程 CLI）。
- 不改 MonitoredProvider 与 session 记账逻辑（双通路各自独立演化）。
- 不自定义 trace/span ID 体系：唯一标识由实现侧生成（OTel 模型自带
  trace_id / span_id / parent_span_id）。

## Decisions

### D1: 直接依赖 OTel API，而非自定义 Tracing 接口

OTel 把"库埋点只依赖 API、app 装配 SDK"作为官方推荐形态；API 模块自带
noop 默认实现，`tracer.Start/span.End` 在 noop 下接近零成本，父子关系经
ctx 白送。自定义接口（Tracing + Reporter 方法）需要自造 ctx 传播、
span 树维护与批量导出，等于重做 SDK 已解决的问题；薄封装包一层 OTel
则两头不讨好（要么泄漏 OTel 类型形同没包，要么自建转译层）。

"Reporter 何时被调用"在该决策下的最终答案：laxcode 埋点只调
`Start/SetAttributes/End`；**实现侧 `Span.End()` 即上报触发点**（每个
span 同步一次）；若实现方是官方 SDK，则 End → SpanProcessor 入队 →
BatchSpanProcessor 攒批异步导出，批量策略由实现方配置。

### D2: 扩展点 = `trace.TracerProvider` 注入 + 接口断言探测 Shutdown

- `AgentEngine` 与 `tools.DefaultRegistry` 新增 `trace.Tracer` 字段，
  构造期传入；**nil 即缺省为 noop tracer**——现有测试与调用点零改动。
- `internal/tracing` 包提供装配辅助：从 `trace.TracerProvider` 派生
  Tracer，并用接口断言探测可选的 `Shutdown(context.Context) error`
  （SDK 的 TracerProvider 具备此方法，API 接口本身没有）；noop 默认下
  Shutdown 为空操作。main 持有该辅助句柄，在两条退出路径 defer Shutdown。
- 用户接入路径有两条，埋点代码均不感知：
  a) 自行实现 `trace.TracerProvider` 接口并注入（完全自定义上报逻辑）；
  b) 在使用方进程装配官方 SDK 后把 `otel.GetTracerProvider()` 注入。

### D3: span 树与属性规范

```
交互模式（每次用户输入一个新 trace）     one-shot 模式（整任务一个 trace）

terminal-task  [root]                     agent-run  [root]
└─ agent-run                              └─ react-loop (loop_seq=1)
   ├─ react-loop (loop_seq=1)                 ├─ llm-generate
   │  ├─ llm-generate                         └─ tool-exec ×N
   │  └─ tool-exec ×N (可并行兄弟)         ├─ react-loop (loop_seq=2)
   ├─ react-loop (loop_seq=2)            └─ ...
   └─ ...
```

| span | 业务属性 | 耗时 | token |
|---|---|---|---|
| terminal-task（仅交互模式） | session_id、task_seq | ✓ | ✓ 本轮合计 |
| agent-run | session_id、agent_role(main/sub) | ✓ | ✓ 合计 |
| react-loop | session_id、loop_seq | ✓ | ✓ 本轮 |
| llm-generate | session_id、tool_call_count | ✓ | ✓ input/output |
| tool-exec | session_id、tool_name、is_error | ✓ | ✗（按需求不记） |

- 属性 key 分两类：token / model 等通用概念尽量采用 OTel GenAI 语义约定
  键名（`gen_ai.usage.input_tokens` 等，experimental），laxcode 自有概念
  用 `laxcode.*` 前缀（`laxcode.session_id`、`laxcode.tool_name` 等）。
  全部 key 集中在 `internal/tracing/attrs.go` 定义为常量。
- terminal-task 的 token 合计 = Run 前后 `sess.TokenUsed` 差值；
  agent-run / react-loop 同理取差值或在循环内累计。
- span 名与属性 key 即"本地语义约定"，全部收敛在 tracing 包，埋点处
  只引用常量，不散落字面量。

### D4: 埋点位置收敛在 Run / executeToolCalls / Registry，不动 MonitoredProvider

Run 内 `Generate` 返回后 `msg.TokenUsed` 直接可读，无需为取数把 tracer
塞入装饰器层。MonitoredProvider 继续承担 session 本地记账（meta.json），
与 tracing 上报通路互不感知、各自演化。

### D5: 并行 read_file 分支沿用传入 ctx

fork-join goroutine 内的 `Registry.Execute` 以同一 react-loop 的 ctx
创建 span，天然生成时间轴重叠的兄弟 span；OTel 模型原生支持，无需
特殊处理。

### D6: 错误映射

LLM 生成失败、工具执行失败在对应 span 上 `RecordError` +
`SetStatus(Error)`；`too_many_turns` 在 agent-run span 上记录为错误
（交互模式下 Run 返回该错误但 loop 继续，属可观测的异常终态）。

## Risks / Trade-offs

- [one-shot 进程存活短于批量导出间隔，尾部 span 丢失] → main 两条退出
  路径 defer Shutdown（one-shot 路径在写契约 JSON 之前 flush，避免
  stdout 契约输出后进程直接退出）。
- [GenAI semconv 属性键 experimental，未来可能改名] → 键名集中在
  tracing/attrs.go 常量，演进时单点修改。
- [注入真实 SDK 后 span.End 进入热路径] → SDK 的 OnEnd 为内存入队
  （微秒级）；noop 时开销为零。交互模式的瓶颈是网络 LLM 调用，观测
  开销可忽略。
- [AgentEngine / DefaultRegistry 构造函数参数继续膨胀] → 遵循项目现有
  直接传参风格加参，nil 缺省 noop 保证向后兼容；若未来参数继续增多，
  再统一评估 functional options（不在本期范围）。
- [terminal-task token 差值依赖 Session 单写者假设] → 现架构 Session
  本就非并发安全（见 session.go 注释），tracing 遵循同一假设，未来
  并发前端落地时一并处理。

## Open Questions

- 是否在 span 属性 / event 中记录工具参数摘要或 LLM 输入摘要（可调试性
  vs 隐私与体积）：本期只记元数据，内容级记录留待有真实后端后评估。
- Metrics（Meter）接入点：当需要跨 session 的全局聚合（QPS、P99、
  token 速率）时再引入，预计复用同一注入机制（MeterProvider 字段）。

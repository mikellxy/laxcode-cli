## Why

laxcode 目前只有交互式 REPL（TerminalLoop），无法被脚本、CI 或上游 Agent 以"单次调用、结构化取结果"的方式编排。引入 one-shot 模式后，一次进程启动执行一轮完整任务并以 JSON 返回结果与 token 统计，laxcode 即可作为无状态函数被外部系统组合调用（配合 `-session` 还可串起多轮）。

## What Changes

- 新增 one-shot CLI 模式（`cmd/main/main.go` + `internal/engine/engine.go`）：
  - 新增四个命令行参数（仅命令行来源，忽略环境变量与 settings.json，沿用 `-session`/`-plan` 的 config 包声明风格）：`-oneshot`（开关）、`-task`（提示词）、`-task-file`（提示词文件路径，优先级高于 `-task`）、`-workdir`（工作目录，one-shot 模式下必填）
  - `internal/engine` 新增与 `TerminalLoop` 平级的 `OneShotLoop`：把提示词追加进 session 后调用一次 `Run`，结束后向 stdout 输出单行 JSON
  - 结构化返回：成功与失败共用同一扁平结构——`session_id`、`result`（Run 的文本结果）、`token_used`、`window_token`（均复用 `schema.TokenStatistics`）、`error`（成功时为 null）；不返回 rounds。失败时 `error` 携带机器可读的 `type`（`usage`/`generate`/`too_many_turns`）与 `message`，并以非零 exit code 退出
  - 输出契约：one-shot 模式下 stdout 有且仅有最终 JSON；默认完全静默（装配前 `SetDefault(DiscardPrinter{})`），`-verbose` 时中间过程送往 stderr
- 新增 `internal/printer` 叶子包，统一收口全部人类可读输出：
  - `Printer` 接口（`Printf`/`PrintLLM`/`PrintToolCall`/`PrintCompressResult`/`WithColors`）+ `WriterPrinter`（writer + 配色 + mutex 串行化写，修复 read_file fork-join 并发打印交错隐患）与 `DiscardPrinter`（one-shot 静默实现）
  - `AgentEngine.Printer` 字段注入实例（替代原 `PrintLLM` 字段），engine 内 LLM 消息/压缩提示经它输出；`NewDefaultRegistry` 构造注入 Printer，工具调用提示经它输出
  - session/skill 警告与 config 调试等无 engine 宿主的散点走包级默认实例（`SetDefault`/`Default`）+ 包级委托函数
  - 子 Agent 配色经 `WithColors` 派生同目的地换色实例，one-shot 下自动继承静默/stderr
  - `tools.BaseTool.AfterExecInfo` 本次保持现状不动
- 交互模式行为零变化：printer 默认目的地为 stdout，TerminalLoop 的观感与现状一致

## Capabilities

### New Capabilities
- `engine/one-shot`: one-shot CLI 模式——命令行参数解析（one-shot/task/task-file/workdir/verbose）、单次 Run 执行、stdout 结构化 JSON 返回（成功/失败同构）、exit code 语义、静默/verbose 输出控制
- `printer/output`: 人类可读输出的统一收口——Printer 接口与实例注入（WriterPrinter/DiscardPrinter）、AgentEngine.Printer 与 Registry 的注入关系、包级默认实例覆盖散点、并发安全写

### Modified Capabilities
<!-- 无：engine/session、engine/token-usage、tools/* 等现有 spec 的需求均不受影响 -->

## Impact

- `cmd/main/main.go`：改造（新增四个参数声明、one-shot 分支装配、workdir 解析与必填校验、usage 错误的 JSON 输出与 exit code）
- `internal/engine/engine.go`：改造（新增 OneShotLoop、Run 内压缩提示改走 printer）
- `internal/engine/oneshot.go`（新增）：OneShotLoop 与结构化返回类型；`internal/engine/printer.go`：改造为 printer 包薄壳
- `internal/printer/`：新增叶子包（仅依赖 `internal/schema` 与标准库，不产生 import 环）
- `internal/tools/registry.go`：改造（tool 执行提示改走 printer）
- `internal/engine/session.go`、`internal/context/skill.go`、`internal/config/config.go`：改造（警告/调试输出改走 printer）
- `openspec/config.yaml` 的 context 需补充 `printer` 子包说明（约定"新功能归入 internal 子包"，本次新增一个子包）
- 无外部依赖变化；provider 包零改动

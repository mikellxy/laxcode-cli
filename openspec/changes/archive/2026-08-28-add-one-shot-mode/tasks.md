# Tasks

## 1. 新建 internal/printer 包

- [x] 1.1 创建 `internal/printer/printer.go`：`Printer` 接口（`Printf`/`PrintLLM`/`PrintToolCall`/`PrintCompressResult`/`WithColors`）、`WriterPrinter` 实现（writer + 配色 + mutex 串行化写）、`DiscardPrinter` 静默实现、包级默认实例与 `SetDefault`/`Default`、包级委托函数 `Warnf`/`Errorf`/`Debugf`/`Printf`；仅依赖 `internal/schema` 与标准库
- [x] 1.2 实现 `NewMainPrinter()`（stdout + 主 Agent 配色）与 `NewWriterPrinter(w, thinkColor, contentColor)`；`WithColors` 派生同目的地换色实例
- [x] 1.3 实现分级输出格式：`Warnf`（黄 `[LaxCode]` 前缀）、`Errorf`（红 `[LaxCode][WARN]` 前缀）、`Debugf`（灰 `[debug]` 前缀，保留仅 debug 开启时输出的语义）
- [x] 1.4 编写 `internal/printer/printer_test.go`：WriterPrinter 目的地与格式、DiscardPrinter 静默、WithColors 派生、SetDefault 切换、并发写不交错

## 2. 各打印点切换到 Printer 实例

- [x] 2.1 `AgentEngine` 增加 `Printer` 字段（`NewAgentEngine` 默认取 `printer.Default()`），`Run` 内 LLM 消息与压缩提示改经 `engine.Printer`；`TerminalLoop` 的 banner/提示符/EOF/quit/warn 打印改经 `agentEngine.Printer`；删除 `PrintLLM` 字段与 `internal/engine/printer.go`（子 Agent 配色改为 `WithColors` 派生）
- [x] 2.2 `NewDefaultRegistry` 构造注入 `printer.Printer`（默认 `printer.Default()`），`Execute` 中的工具调用提示改为 `p.PrintToolCall(tool.BeforeExecInfo(...))`；适配 main/subagent/测试全部调用点
- [x] 2.3 `internal/engine/session.go` 四个 warn 函数与 `internal/context/skill.go` 的 skill 警告改为包级 `printer.Warnf`/`printer.Errorf`（黄/红级别与原文案保持一致）
- [x] 2.4 `internal/config/config.go` 的 `Debugf` 改为委托 printer 包级函数（调用方签名不变）
- [x] 2.5 `cmd/main/main.go` 启动横幅改走 printer 包级函数
- [x] 2.6 交互模式回归验证：REPL 观感（配色、前缀、提示符）与收口前一致；`go build ./...` 与既有测试通过

## 3. one-shot 参数解析

- [x] 3.1 `cmd/main/main.go` 声明五个 Flag-only 配置项（`Item{Flag: ...}`，`Env`/`Key` 留空）：`ONESHOT`(bool)、`TASK`(string)、`TASKFILE`(string)、`WORKDIR`(string)、`VERBOSE`(bool)
- [x] 3.2 workdir 解析：`-workdir` 非空则用其值，否则维持 `os.Getwd()`；session 目录创建（`MkdirAll`）、工具注册、`GetSession` 统一改用解析后的 workdir
- [x] 3.3 one-shot 参数校验：`-oneshot` 启用时 `-workdir` 必填；提示词解析（`-task-file` 非空则读文件且优先，否则取 `-task`），两者皆空或文件读取失败判为 usage 错误
- [x] 3.4 one-shot 模式下设置输出闸门：装配引擎前 `printer.SetDefault(DiscardPrinter{})`，携带 `-verbose` 时 `printer.SetDefault(NewWriterPrinter(os.Stderr, ...))`

## 4. OneShotLoop 与结构化返回

- [x] 4.1 新建 `internal/engine/oneshot.go`：定义扁平同构返回结构（`session_id`/`result`/`token_used`/`window_token`/`error`，token 字段内嵌复用 `schema.TokenStatistics`，不含 rounds）与错误类型常量（`usage`/`too_many_turns`/`generate`）
- [x] 4.2 实现 `OneShotLoop(ctx, agentEngine, task)`：Append user 消息 → 调 `Run` → 组装结果 JSON 直写 `os.Stdout`（不经 Printer）→ 按结果返回 nil 或映射后的错误
- [x] 4.3 错误映射：`errors.Is(err, errTooManyTurns)` → `too_many_turns`；其余 Run 错误 → `generate`；失败返回中 `result` 为空串但保留 `session_id` 与截至失败时的 token 统计
- [x] 4.4 `cmd/main/main.go` 接入：one-shot 分支调 `OneShotLoop`；成功 exit 0、运行失败 exit 1、用法错误 exit 2；one-shot 路径不 panic，错误一律走 JSON + exit code
- [x] 4.5 编写 `internal/engine/oneshot_test.go`：成功/失败返回结构序列化正确、错误类型映射正确、失败仍带 token 统计、rounds 不出现在 JSON 中

## 5. 端到端验证与收尾

- [x] 5.1 端到端验证 one-shot：成功任务（exit 0、stdout 仅一行 JSON、默认 stderr 无输出）、`-verbose`（中间过程在 stderr、stdout 仍仅 JSON）、缺 `-workdir`（exit 2 + usage JSON）、配合 `-session` 续跑（session_id 一致）
- [x] 5.2 全量测试与构建：`go build ./...`、`go test ./...` 通过
- [x] 5.3 更新 `openspec/config.yaml` 的 context：internal 子包清单补充 `printer`（人类可读输出统一收口）

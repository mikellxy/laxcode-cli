## Context

现状约束（详见 proposal.md 的 Why）：

- 打印分散在 6 处：模型消息（`engine/printer.go`，经 `AgentEngine.PrintLLM` 注入）、工具调用提示（`tools/registry.go:55` 硬编码）、压缩提示（`engine.Run` 内硬编码）、session/skill 包级警告函数、`config.Debugf`、main 横幅与 REPL 交互
- 依赖方向：`schema`/`config`/`context` 是叶子；`tools` 依赖 `context`；`engine` 依赖全部。printer 要同时被 engine 与 tools 使用，**不能住进 engine**（否则 tools→engine 成环）
- `AgentEngine.PrintLLM` 字段承载主/子 Agent 配色差异（主：灰/绿；子：紫），是 per-engine 关注
- `config` 包的 `Item` 只填 `Flag`、留空 `Env`/`Key` 即天然忽略环境变量与 settings.json（`-session`/`-plan` 已有先例）
- bash 工具经 `CombinedOutput()` 捕获子进程输出，子进程不直接写 stdout/stderr，输出面可控

## Goals / Non-Goals

**Goals:**

- one-shot 模式：单次 Run + stdout 单行 JSON 结构化返回 + 非零 exit code 表失败
- 新建 `internal/printer` 叶子包收口全部人类可读输出，目的地一处切换
- one-shot 默认静默（丢弃中间输出），`-verbose` 切换到 stderr
- 交互模式观感零变化

**Non-Goals:**

- `tools.BaseTool.AfterExecInfo`（现为死代码）保持现状，去留另开 change
- 不做 http/sse 等其它前端 loop
- 不改变 token 统计口径、session 持久化格式与 REPL 交互逻辑
- 不引入日志级别体系（debug 除外，维持现状语义）

## Decisions

### D1: printer 为新建叶子包 `internal/printer`

依赖图决定 printer 必须是无内部依赖（除 `schema`）的叶子包。备选：塞进 `internal/context`（职责混杂：token 监控 + prompt 组装与输出无关）或 `internal/config`（配置与输出是两个关注）——均拒绝。openspec context 约定"新功能归入 internal 子包"，本变更新增一个子包，archive 时需同步 `openspec/config.yaml` 的包清单。

### D2: Printer 接口 + 实例注入，包级默认实例覆盖散点

printer 包定义 `Printer` 接口（`Printf` / `PrintLLM` / `PrintToolCall` / `PrintCompressResult` / `WithColors`）与两个实现：`WriterPrinter`（writer + 可注入配色，写操作 mutex 串行化，顺带修复 `executeToolCalls` fork-join 并发打印的交错隐患）与 `DiscardPrinter`（one-shot 静默实现，全部输出为空操作）。输出控制不靠全局 writer 开关，而是**注入不同 Printer 实例**：

- `AgentEngine.Printer` 字段持有实例，engine 内全部打印（LLM 消息、压缩提示）经它，替代原 `PrintLLM` 字段
- 工具调用提示的宿主是 registry（`BeforeExecInfo` 是 tool 的方法、tool 查找在 registry 内部，engine 拿不到 tool 实例，无法上移），故 `NewDefaultRegistry` 构造注入 Printer
- session/skill 警告与 config 调试是无 engine 宿主的散点：printer 包级 `defaultPrinter` + `SetDefault` 切换，包级 `Warnf`/`Errorf`/`Debugf`/`Printf` 委托它
- 子 Agent 配色经接口的 `WithColors(think, content)` 派生同目的地换色实例——one-shot 下子 Agent 自动继承静默/stderr，无漏网点

被拒备选：包级 `SetOut(io.Writer)` 全局开关（隐式全局状态，无法按实例 mock）；Printer struct 穿透 session/skill 纯函数（为输出穿对象不值得，散点走包级默认实例已足够）。

### D3: one-shot 的输出闸门 = 装配前一次 SetDefault

one-shot 模式在 main 装配引擎前调用一次 `printer.SetDefault`：默认注入 `DiscardPrinter{}`（完全静默），`-verbose` 时注入 stderr 版 `WriterPrinter`。随后 `NewAgentEngine`/`NewDefaultRegistry` 默认取 `printer.Default()`，engine、registry、散点警告全部落在同一实例上——一次注入即闭合"stdout 只有 JSON"契约。交互模式不触碰 default（`NewMainPrinter()` = stdout + 主 Agent 配色），观感不变。one-shot 的结果 JSON 是契约出口，直写 `os.Stdout`、不经 Printer（见 D4）。

### D4: OneShotLoop 与 TerminalLoop 平级，落 `internal/engine`（新文件 `oneshot.go`）

流程：`sess.Append(user 消息)` → `Run(ctx)` → 组装结果 JSON → **直写 `os.Stdout`**（不经 printer——printer 目的地可能指向 stderr/丢弃，JSON 是契约出口，刻意绕过）。session 装配复用 main 现有的 `GetSession` 路径，天然支持 `-session` 续跑。

### D5: 结构化返回为扁平同构结构 + error 字段

```json
{"session_id":"...","result":"...","token_used":{...},"window_token":{...},"error":null}
```

- 成功/失败共用一套 schema，调用方 `error != null` 即失败；失败也带 token 统计（钱已花，调用方有权知道）——信封式 `{"ok":...}` 在错误分支放不下统计字段，拒绝
- `token_used`/`window_token` 直接内嵌复用 `schema.TokenStatistics`（自带 json tag），`rounds` 不定义即不返回
- `error.type` 机器可读：`usage` / `too_many_turns`（`errors.Is(err, errTooManyTurns)` 映射）/ `generate`（其余 Run 错误）

### D6: exit code 三值语义

- `0`：成功
- `1`：运行失败（generate / too_many_turns）
- `2`：用法错误（缺 `-workdir`、无提示词、`-task-file` 读取失败——文件路径错误属调用方问题，前置校验阶段可判定）

用法错误发生在 session 建立之前，JSON 中 `session_id` 为空串、token 为零值。main 现有的 `panic` 错误路径在 one-shot 模式下 SHALL 改为结构化 JSON + exit code，不 panic。

### D7: 参数解析沿用 config 包 Flag-only 声明

`-oneshot`(bool)、`-task`(string)、`-task-file`(string)、`-workdir`(string)、`-verbose`(bool) 均以 `Item{Flag: ...}` 声明（`Env`/`Key` 留空），`Get()` 落空即"未提供"。提示词优先级：`-task-file` 非空则读文件，否则取 `-task`。`-workdir` 仅 one-shot 模式必填；交互模式缺省时维持 `os.Getwd()` 兜底，行为不变。

### D8: workdir 解析前置，修复隐含的相对路径问题

main 现在 `os.MkdirAll(".laxcode/.session")` 用的是 cwd 相对路径。引入 `-workdir` 后，session 目录创建、工具注册、sysprompt 构建统一改用解析后的 workdir 绝对路径。

## Risks / Trade-offs

- [未来新增打印点绕过 printer 直写 stdout，破坏 one-shot JSON 契约] → spec 已立"必须经 printer"的规范性要求；printer 收口后 `fmt.Print*` 在业务代码中出现即评审信号
- [默认静默使失败现场不可见] → 错误信息完整承载于 JSON `error` 字段；`-verbose` 提供逃生门
- [包级可变状态（writer）是全局单例] → 进程内只有一个前端 loop（REPL 或 one-shot 二选一），单进程单目的地是真实语义；并发前端落地时与 session 锁一并重新评估
- [flag 包遇位置参数停止解析] → 不使用位置参数传提示词，全部为 flag，规避顺序陷阱

## Migration Plan

纯增量：交互模式默认目的地为 stdout、格式与配色不变，用户无感知；无数据迁移。printer 收口按"先建包、再逐点切换、最后删旧实现"的顺序落地，每步可编译可测试。

## Open Questions

无。

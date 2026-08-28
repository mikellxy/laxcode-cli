## Purpose

将 laxcode 全部人类可读输出（模型消息、工具调用提示、压缩提示、警告、调试信息）统一收口到 Printer 接口，运行期以注入不同实现控制输出行为（stdout/stderr/静默），为 one-shot 模式的"stdout 只有 JSON"契约与静默/verbose 控制提供基础。

## ADDED Requirements

### Requirement: 输出统一收口

全部人类可读输出 SHALL 经由 Printer 实例输出，包括：assistant 消息的 thinking 与正文、工具调用前提示、上下文压缩提示、session 历史/快照读写警告、skill 加载警告、调试信息、REPL 交互提示与启动横幅。各输出点 MUST NOT 绕过 Printer 直接写 stdout/stderr。

#### Scenario: 模型消息经引擎持有的 Printer 输出
- **WHEN** 一次模型调用返回 assistant 消息（含 thinking 或正文）
- **THEN** 该消息经引擎持有的 Printer 实例以既有配色格式输出，不直接写 stdout

#### Scenario: 工具调用提示经注册表持有的 Printer 输出
- **WHEN** 工具注册表执行任一工具调用
- **THEN** 执行前提示经注册表持有的 Printer 实例输出

### Requirement: Printer 接口与实例注入

输出能力 SHALL 以 Printer 接口抽象，运行期通过注入不同实现控制输出行为：写往指定目的地的实现（stdout/stderr 等，配色可注入）与全静默实现（所有输出为空操作）。引擎与工具注册表 SHALL 持有注入的 Printer 实例；无明确宿主的散点输出（session/skill 警告、调试信息）SHALL 经由包级默认实例，该默认实例 SHALL 可整体替换，替换后引擎、工具注册表与散点输出 SHALL 落在同一行为上——这是 one-shot 模式静默/verbose 控制的唯一闸门。

#### Scenario: 注入静默实现
- **WHEN** 默认实例被替换为静默实现后运行一轮任务
- **THEN** 中间输出不产生任何 stdout/stderr 内容

#### Scenario: 注入 stderr 实现
- **WHEN** 默认实例被替换为 stderr 实现后产生任意中间输出
- **THEN** 该输出出现在 stderr，stdout 无对应内容

#### Scenario: 子实例继承目的地仅换配色
- **WHEN** 从某 Printer 实例派生仅更换配色的子实例并用于子 Agent
- **THEN** 子 Agent 输出配色不同但目的地与父实例一致

### Requirement: 并发写安全

同一 Printer 实例的写操作 SHALL 串行化：多个 goroutine 并发输出时（如多个 read_file 并行执行的调用提示），单次输出的内容 MUST NOT 与其他输出交错混杂。

#### Scenario: 并行工具调用提示不交错
- **WHEN** 一轮中多个 read_file 调用并发执行并各自输出提示
- **THEN** 每条提示完整成行，不出现行内字符交错

### Requirement: 交互模式行为保持

交互模式默认实例下，输出格式 SHALL 与收口前一致：assistant 消息保留主/子 Agent 配色区分（主 Agent thinking 灰、正文绿；子 Agent 紫），警告保留黄/红级别与 `[LaxCode]` 前缀，REPL 提示符与横幅观感不变。主/子 Agent 的配色差异 SHALL 由同目的地、不同配色的 Printer 实例表达。

#### Scenario: 主 Agent 与子 Agent 配色可区分
- **WHEN** 交互模式下子 Agent 执行并产生 assistant 消息
- **THEN** 子 Agent 消息以紫色输出，主 Agent 消息保持灰/绿，与收口前一致

#### Scenario: 警告级别与收口前一致
- **WHEN** 会话历史写盘失败
- **THEN** 输出红色 WARN 级警告；普通警告（如跳过坏行）为黄色，均与收口前一致

## Purpose

为 laxcode 提供 one-shot（单次执行）CLI 模式：一次进程启动执行一轮完整任务，以单行 JSON 向 stdout 返回结果与 token 统计，使 laxcode 可被脚本、CI 或上游 Agent 以无状态函数的方式编排调用。

## Requirements

### Requirement: one-shot 模式命令行参数

one-shot 模式 SHALL 通过命令行参数 `-oneshot`（开关）启用，任务提示词 SHALL 由 `-task`（直接文本）或 `-task-file`（提示词文件路径）提供；两者同时提供时 `-task-file` MUST 优先。one-shot 模式下 `-workdir` SHALL 为必填，作为工具执行与 session 存储的工作目录。`-oneshot`、`-task`、`-task-file`、`-workdir` 与 `-verbose` 这五个参数 SHALL 只支持命令行来源，MUST NOT 从环境变量或 settings.json 读取。未启用 `-oneshot` 时，现有交互式 REPL 行为 MUST 保持不变。

#### Scenario: 以 -task 启用 one-shot
- **WHEN** 启动命令为 `laxcode -oneshot -workdir /path -task "修复登录bug"`
- **THEN** 不进入交互式 REPL，以 "修复登录bug" 为用户输入执行一轮任务后退出

#### Scenario: -task-file 优先于 -task
- **WHEN** 启动命令同时携带 `-task "文本"` 与 `-task-file prompt.md`
- **THEN** 以 prompt.md 的文件内容为用户输入，`-task` 的值被忽略

#### Scenario: one-shot 模式缺省 -workdir 报用法错误
- **WHEN** 启动命令携带 `-oneshot -task "..."` 但未携带 `-workdir`
- **THEN** 进程不执行任务，输出 usage 类错误并以非零 exit code 退出

#### Scenario: one-shot 模式未提供提示词报用法错误
- **WHEN** 启动命令携带 `-oneshot -workdir /path` 但 `-task` 与 `-task-file` 均未提供或内容为空
- **THEN** 进程不执行任务，输出 usage 类错误并以非零 exit code 退出

#### Scenario: 参数不走环境变量与 settings.json
- **WHEN** 环境变量或 settings.json 中存在 ONESHOT/TASK/TASKFILE/WORKDIR/VERBOSE 同名配置，但命令行未传对应参数
- **THEN** 这些配置不生效，进程按未传参处理

### Requirement: 单次执行语义

one-shot 模式 SHALL 把提示词作为一条 user 消息追加进 session 历史（照常落盘 history.jsonl），随后执行一次完整的"生成-工具"循环直至模型给出无工具调用的最终回答，然后退出进程。one-shot 模式 SHALL 与 `-session` 参数兼容：指定已有 session id 时在该会话历史上续跑。

#### Scenario: 一次调用完成一轮任务
- **WHEN** one-shot 模式下模型经若干次工具调用后给出最终回答
- **THEN** 全部消息按序落盘到 session 历史，进程输出结果后正常退出

#### Scenario: 配合 -session 续跑已有会话
- **WHEN** 启动命令为 `laxcode -oneshot -workdir /path -session <已有id> -task "继续"`
- **THEN** 任务在该 session 的已有历史上继续执行，返回的 session_id 为同一 id

### Requirement: 结构化返回格式

one-shot 模式结束时 SHALL 向 stdout 输出恰好一个 JSON 对象作为唯一 stdout 内容。成功与失败 SHALL 共用同一扁平结构：`session_id`（字符串）、`result`（Run 的最终文本，失败时为空串）、`token_used`（会话累计 token 用量）、`window_token`（上下文窗口占用快照）、`error`（成功时为 null）。token 统计 SHALL 与 session 统计口径一致，`rounds` MUST NOT 出现在返回中。

#### Scenario: 成功返回
- **WHEN** 任务正常完成
- **THEN** stdout 输出单个 JSON 对象，`result` 为最终回答文本，`session_id` 为本次会话 id，`token_used`/`window_token` 为当前 session 统计，`error` 为 null，exit code 为 0

#### Scenario: 失败仍返回 token 统计
- **WHEN** 任务执行中途失败（如模型调用报错）
- **THEN** stdout 仍输出同一结构的 JSON：`result` 为空串，`session_id` 与截至失败时的 `token_used`/`window_token` 照常返回，`error` 非空

### Requirement: 错误语义与 exit code

one-shot 模式失败时，`error` 字段 SHALL 携带机器可读的 `type` 与 `message`，并以非零 exit code 退出。`type` SHALL 至少区分：`usage`（参数用法错误，此时进程尚未建立 session，`session_id` 为空、token 统计为零值）、`too_many_turns`（单轮工具循环达到上限）、`generate`（模型调用失败）。交互模式下的 `too_many_turns` 是警告并继续，one-shot 模式下 SHALL 视为终态失败。

#### Scenario: 用法错误
- **WHEN** 参数校验失败（缺 `-workdir` 或无提示词）
- **THEN** stdout 输出 `error.type` 为 `usage` 的 JSON，`session_id` 为空串，exit code 非零

#### Scenario: 工具循环超限
- **WHEN** 单轮工具循环达到轮次上限
- **THEN** stdout 输出 `error.type` 为 `too_many_turns` 的 JSON，已产生的 session 历史与 token 统计保留，exit code 非零

### Requirement: 静默与 verbose 输出控制

one-shot 模式 SHALL 默认完全静默：中间过程（模型输出、工具调用提示、压缩提示、警告等）MUST NOT 输出到 stdout 或 stderr。携带 `-verbose` 时，中间过程 SHALL 输出到 stderr。无论是否 verbose，stdout SHALL 只有最终 JSON。

#### Scenario: 默认静默
- **WHEN** one-shot 模式未携带 `-verbose` 运行
- **THEN** stdout 与 stderr 均无中间过程输出，stdout 仅有最终 JSON

#### Scenario: verbose 输出到 stderr
- **WHEN** one-shot 模式携带 `-verbose` 运行
- **THEN** 模型输出、工具调用提示等中间过程出现在 stderr，stdout 仍仅有最终 JSON

### Requirement: 用法错误的可诊断性

用法错误的 JSON 返回 SHALL 在 `error.message` 中说明缺失或冲突的参数，使调用方无需查看 stderr 即可修正调用。

#### Scenario: 缺少 -workdir 的错误信息
- **WHEN** one-shot 模式缺省 `-workdir`
- **THEN** 返回 JSON 的 `error.message` 指明 `-workdir` 为必填参数

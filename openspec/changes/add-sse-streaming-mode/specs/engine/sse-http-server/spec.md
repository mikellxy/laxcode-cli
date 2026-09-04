## Purpose

定义 laxcode 的第三种交互模式——SSE 流式服务模式：进程以 HTTP server 形态常驻，客户端经 POST 携带会话标识与任务发起请求，服务端边跑 ReAct 循环边以 Server-Sent Events 流式回吐增量，使 laxcode 可被前端或上游系统以流式方式编排调用。

## ADDED Requirements

### Requirement: SSE 模式开关

系统 SHALL 通过命令行参数 `-sse`（开关）启用 SSE 服务模式；置位时进程 SHALL 只启动 HTTP server，MUST NOT 进入交互式 REPL，也 MUST NOT 执行 one-shot。`-sse` SHALL 只支持命令行来源，MUST NOT 从环境变量或 settings.json 读取。未置位 `-sse` 时，现有交互式与 one-shot 行为 MUST 保持不变。

#### Scenario: -sse 启动常驻 server

- **WHEN** 启动命令携带 `-sse`
- **THEN** 进程启动 HTTP server 并常驻监听，不进入 REPL、不执行 one-shot

#### Scenario: 未置 -sse 保持既有模式

- **WHEN** 启动命令未携带 `-sse`
- **THEN** 进程按既有逻辑进入交互 REPL 或（携带 `-oneshot` 时）执行 one-shot，不启动 server

### Requirement: POST 请求契约

SSE 端点 SHALL 仅接受 POST 请求，请求体 SHALL 携带会话标识（`session_id`）与任务提示词（`prompt`）。`prompt` 缺失或为空时 SHALL 以错误响应拒绝，不启动运行。`session_id` 缺失时 SHALL 由服务端自动生成（毫秒精度时间串）。非 POST 方法 SHALL 被拒绝。

#### Scenario: 合法 POST 触发一轮流式运行

- **WHEN** 客户端以 POST 携带非空 `prompt` 与一个 `session_id`
- **THEN** 服务端在该会话上以 `prompt` 为用户输入启动一轮流式运行，并以 SSE 回吐事件

#### Scenario: 缺 prompt 被拒绝

- **WHEN** POST 请求体未携带 `prompt` 或其内容为空
- **THEN** 服务端返回错误响应，不启动运行

#### Scenario: 非 POST 方法被拒绝

- **WHEN** 客户端以 GET 等非 POST 方法访问 SSE 端点
- **THEN** 服务端拒绝该请求

### Requirement: 每请求装配且不预注入会话

服务端 MUST NOT 在启动时预注入单一会话。每个请求 SHALL 以其 `session_id` 经会话库取得对应会话（缓存命中复用、未命中从磁盘重放），并据此装配该请求所需的引擎与工具集；请求结束后 SHALL 回收该请求装配的带生命周期工具资源（后台进程与临时文件）。

#### Scenario: 每请求按 id 取得会话

- **WHEN** 两个先后到达的请求携带不同 `session_id`
- **THEN** 各自取得对应会话并独立装配引擎运行，互不共享会话历史

#### Scenario: 请求结束回收工具资源

- **WHEN** 一次请求的运行结束（成功、失败或客户端断连）
- **THEN** 该请求装配的工具资源被回收，不遗留后台进程或临时文件

### Requirement: SSE 流式响应格式

响应 SHALL 以 `text/event-stream` 推送。每个运行事件 SHALL 序列化为恰好一行 `data:` 帧、帧内为携带 `type` 判别式的 JSON 对象，并在写出后及时 flush 使客户端实时收到。一次运行的事件流 SHALL 以运行成功收尾或运行失败收尾事件作为终止信号，MUST NOT 追加额外的 `[DONE]` 类哨兵帧。

#### Scenario: 事件逐帧实时 flush

- **WHEN** 运行过程中产生增量事件
- **THEN** 每个事件各作为一个 `data:` JSON 帧写出并 flush，客户端在生成过程中即陆续收到

#### Scenario: 正常收尾

- **WHEN** 运行成功得到最终回答
- **THEN** 事件流以携带最终结果与 token 统计的运行成功收尾事件结束

#### Scenario: 失败收尾

- **WHEN** 运行中途失败
- **THEN** 事件流以携带机器可读错误信息的运行失败收尾事件结束

### Requirement: SSE 模式静默输出

SSE 模式 SHALL 默认静默：模型输出、工具调用提示、压缩提示、警告等中间过程 MUST NOT 写入 stdout 或 stderr，SSE 事件流是面向客户端的唯一输出通道。

#### Scenario: 中间过程不落标准流

- **WHEN** SSE 模式下运行一轮含工具调用的任务
- **THEN** stdout 与 stderr 均无中间过程输出，客户端只经 SSE 帧收到内容

### Requirement: workDir 固定为启动目录

SSE 模式的工作目录 SHALL 在 server 启动时解析一次（缺省为进程当前工作目录）并对全部请求固定；单个请求 MUST NOT 覆盖工作目录。会话持久化与工具执行 SHALL 均以此固定目录为根。

#### Scenario: 全请求共用启动目录

- **WHEN** server 在某目录启动后接到多个请求
- **THEN** 全部请求的会话存储与工具执行都以该启动目录为工作目录

### Requirement: 客户端断连经上下文取消传播

客户端断开连接 SHALL 取消其请求上下文，进而中止服务端正在进行的生成与工具执行，并释放底层流资源。断连 MUST NOT 使服务端进程崩溃或泄漏该请求的资源。

#### Scenario: 断连中止运行

- **WHEN** 客户端在运行过程中断开连接
- **THEN** 服务端中止该请求的后续生成与工具执行，回收其资源，进程继续服务其他请求

### Requirement: 优雅关停与 server 级 tracer 复用

server SHALL 在收到进程终止信号时优雅关停，停止接收新请求并回收带生命周期的工具资源。SSE 模式 SHALL 在启动时装配单一 tracer 供全部请求复用；按会话分别落盘 tracing 不在本版范围。

#### Scenario: 终止信号优雅关停

- **WHEN** server 运行期间收到终止信号
- **THEN** server 停止接收新请求、回收工具资源后退出

#### Scenario: 全请求共用 tracer

- **WHEN** server 处理多个请求
- **THEN** 各请求的追踪 span 都经启动时装配的同一 tracer 创建

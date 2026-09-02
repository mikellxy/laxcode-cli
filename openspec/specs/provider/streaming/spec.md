## Purpose

定义 provider 层的流式生成能力：在模型生成过程中实时向外推送领域级增量事件，同时累积出与批式生成语义等价的完整消息，供未来 HTTP SSE 等流式消费者对接，且不影响现有批式链路。

## Requirements

### Requirement: 流式生成接口

系统 SHALL 提供一个可选的流式生成能力 `GenerateStream(ctx, msgs, tools, emit)`：在生成过程中通过 `emit` 回调实时推送增量事件，并在流结束时返回一个完整消息。该能力 SHALL 是可选的——未实现它的 provider 仍 SHALL 能作为批式 provider 被正常使用，消费者可通过能力探测区分二者。

#### Scenario: 能力探测区分流式与批式

- **WHEN** 消费者对一个 provider 探测是否具备流式能力
- **THEN** 实现了流式生成的 provider 被判定为可流式，未实现者被判定为仅批式并可继续走批式路径

#### Scenario: 增量事件与最终消息并存

- **WHEN** 调用流式生成且模型产生正文
- **THEN** `emit` 在生成过程中被多次调用推送增量，且调用返回时得到一个包含完整正文的消息

### Requirement: 流式事件词汇与三段式粒度

系统 SHALL 以带判别式 `Kind` 的事件推送增量，用以区分正文、reasoning、工具调用等语义。正文与 reasoning 内容 SHALL 采用 start/delta/end 三段式：一段内容开始时推送 start 事件，随后每个增量推送 delta 事件，该段结束时推送 end 事件。事件类型 SHALL 与具体传输协议（如 HTTP/SSE）无关，以便上层自由序列化。

#### Scenario: 正文三段式推送

- **WHEN** 模型输出一段正文
- **THEN** 消费者依次收到 正文-start、一个或多个 正文-delta、正文-end 事件

#### Scenario: reasoning 三段式并与正文区分

- **WHEN** 模型输出 reasoning（thinking）内容
- **THEN** 消费者依次收到 reasoning-start、一个或多个 reasoning-delta、reasoning-end 事件，且其 `Kind` 与正文事件不同

### Requirement: 工具调用以完整事件推送

系统 SHALL NOT 流式推送工具调用的参数片段。当模型发起工具调用时，系统 SHALL 在内部累积其参数，待该调用完整后通过单个事件推送完整工具调用（含 id、name 与完整参数）。

#### Scenario: 单个工具调用完整推送

- **WHEN** 模型发起一个工具调用
- **THEN** 消费者收到一个携带完整参数的工具调用事件，而非参数的多个片段

#### Scenario: 一次生成内的多个工具调用

- **WHEN** 模型在同一次生成中发起多个工具调用
- **THEN** 每个调用各推送一个完整事件，且这些调用全部出现在返回消息的工具调用列表中

### Requirement: 最终消息与批式生成等价

流式生成返回的消息 SHALL 与相同输入下批式生成返回的消息语义等价：包含完整正文、reasoning 内容与其标识、全部工具调用，以及本次调用的 token 用量。

#### Scenario: 正文与工具调用聚合正确

- **WHEN** 流式生成结束
- **THEN** 返回消息的正文等于全部正文 delta 依序拼接的结果，工具调用列表等于全部已推送的完整工具调用事件

#### Scenario: token 用量在结束时填充

- **WHEN** 流式生成成功结束
- **THEN** 返回消息的 token 用量被填充为本次调用的输入/输出计数

### Requirement: 取消与错误传播

系统 SHALL 在上下文取消时中止流式生成并释放底层流资源。当生成过程中发生错误时，系统 SHALL 停止推送后续事件并向调用方返回错误。

#### Scenario: 上下文取消中止生成

- **WHEN** 调用方在生成过程中取消传入的上下文
- **THEN** 系统停止推送后续事件、释放流资源，并返回一个反映取消的错误而非无限阻塞

#### Scenario: 中途错误停止推送

- **WHEN** 流在推送若干事件后发生错误
- **THEN** 系统不再推送后续增量事件，并使调用返回一个非 nil 错误

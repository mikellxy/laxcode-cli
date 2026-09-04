## Purpose

定义 engine 层的流式 ReAct 编排能力：`RunSse` 以与批式 `Run` 等价的循环驱动「生成-工具」多轮，但把每一轮的增量与循环边界以领域级事件实时推送给上层消费者（HTTP SSE handler），使客户端边生成边接收，且不影响既有批式链路。

## ADDED Requirements

### Requirement: RunSse 流式循环

系统 SHALL 提供一个流式驱动 `RunSse`，执行与批式 `Run` 相同的「生成-工具」ReAct 循环：每轮组装历史视图、按需压缩上下文、调用生成、把产生消息追加进会话、执行工具调用并把结果追加进会话，直到某轮无工具调用即以该轮正文为最终回答终止。`RunSse` 与 `Run` 的差异 SHALL 仅在输出形状——`RunSse` 经 emit 回调实时推送事件，而非整条批式打印；`RunSse` MUST NOT 依赖人类可读输出通道（Printer）表达任何面向客户端的内容。

#### Scenario: 无工具调用的最终回答终止循环

- **WHEN** 某轮生成不含任何工具调用
- **THEN** `RunSse` 以该轮正文为最终回答终止循环，并推送收尾事件

#### Scenario: 工具调用驱动多轮循环

- **WHEN** 某轮生成含一个或多个工具调用
- **THEN** 系统执行全部工具调用、把结果按调用顺序追加进会话，并进入下一轮生成

#### Scenario: 每轮消息按序落盘

- **WHEN** `RunSse` 运行若干轮
- **THEN** 用户输入、各 assistant 回复（含工具调用）与各工具结果按产生顺序追加进会话历史，落盘序列与批式 `Run` 一致

### Requirement: RunEvent 事件词汇与两层分层

`RunSse` SHALL 经 emit 回调推送带 `type` 判别式的领域级事件。事件 SHALL 分两层：loop 级边界事件由 engine 定义，描述一次运行的编排进度——运行开始、每轮开始、每个工具结果、运行成功收尾、运行失败收尾；生成级增量事件由 provider 的流式增量透传翻译而来——正文、reasoning 各自的开始/增量/结束，以及完整工具调用。事件类型 SHALL 与具体传输协议（如 HTTP/SSE）无关，序列化与帧格式由上层消费者负责。loop 级概念 MUST NOT 下沉进 provider 层。

#### Scenario: 运行边界事件

- **WHEN** `RunSse` 开始与结束一次运行
- **THEN** 消费者先收到携带会话标识的运行开始事件，最终收到运行成功收尾或运行失败收尾事件

#### Scenario: 生成增量透传为正文与 reasoning 事件

- **WHEN** 一轮生成产出正文与 reasoning
- **THEN** 消费者按 provider 推送的三段式增量，依次收到 reasoning 与正文各自的开始/增量/结束事件，且二者 `type` 相互区分

#### Scenario: 工具调用与工具结果分别成事件

- **WHEN** 一轮生成发起工具调用并执行完毕
- **THEN** 消费者先收到携带完整参数（id、name、arguments）的工具调用事件，随后每个工具各收到一个携带输出与错误标记的工具结果事件

### Requirement: 与批式 Run 的输出等价性

相同会话历史与工具集合下，`RunSse` 一次运行的最终文本、追加进会话的消息序列、以及 token 记账结果 SHALL 与批式 `Run` 语义等价。流式路径 MUST NOT 因增量推送而改变落盘消息的内容或记账口径。

#### Scenario: 最终文本等价

- **WHEN** 相同输入分别经 `RunSse` 与 `Run` 运行
- **THEN** 二者得到的最终回答文本一致

#### Scenario: token 记账等价

- **WHEN** `RunSse` 完成若干轮生成
- **THEN** 会话的累计 token 用量与窗口占用统计，与相同轮次经批式 `Run` 得到的结果一致

### Requirement: provider 非流式时的批式降级

当注入的 provider 不具备流式生成能力时，`RunSse` SHALL 降级为批式生成，并把整条回答作为一段正文事件序列（开始→单个增量→结束）推送；loop 级边界事件与工具执行行为 SHALL 保持不变。降级 MUST NOT 使 `RunSse` 失败或静默丢弃回答。

#### Scenario: 非流式 provider 降级为单段正文

- **WHEN** 注入的 provider 未实现流式生成能力
- **THEN** `RunSse` 以批式生成取得整条回答，并把它作为一段正文的开始/增量/结束事件推送，运行照常收尾

### Requirement: 记账内联于循环

token 记账 SHALL 由 ReAct 循环在每次成功生成后直接对会话记账完成，不经任何 provider 装饰器代劳。流式路径的记账 SHALL 取自累积消息在流结束时得到的用量；记账时机 SHALL 与批式一致——生成成功后、该 assistant 消息追加进会话前。生成失败时无用量产生，SHALL 不记账。

#### Scenario: 流式生成成功后记账

- **WHEN** 一轮流式生成成功结束并取得累积消息
- **THEN** 循环以该消息的 token 用量对会话记账，随后把消息追加进会话

#### Scenario: 生成失败不记账

- **WHEN** 一轮生成返回错误
- **THEN** 系统不为该轮记账，且经失败收尾事件与返回值向调用方传播错误

### Requirement: 取消与错误传播

`RunSse` SHALL 在传入上下文被取消（如客户端断连）时中止当前生成与后续轮次，并释放底层流资源。循环中发生的错误 SHALL 既经失败收尾事件推送给消费者，又作为返回值向调用方传播，供其记录日志；错误发生后 MUST NOT 继续推送后续增量事件。

#### Scenario: 上下文取消中止运行

- **WHEN** 调用方在运行过程中取消传入的上下文
- **THEN** `RunSse` 停止后续生成与工具执行、释放流资源，并返回一个反映取消的错误

#### Scenario: 生成错误经事件与返回值双通道传播

- **WHEN** 某轮生成发生错误
- **THEN** 消费者收到失败收尾事件，且 `RunSse` 返回非 nil 错误

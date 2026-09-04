## ADDED Requirements

### Requirement: 会话内消息追加与中断留痕

会话运行期间产生的每条消息——用户输入、assistant 回复（含工具调用）、工具执行结果——SHALL 以产生顺序追加到所属 session 的历史并落盘到 `history.jsonl`。循环因错误中断时，中断前已产生的消息 SHALL 保留在历史中。

<!-- 替代被移除的「会话内消息追加」：原「工具循环超限中断保留现场」Scenario 依赖已废弃的 turn 上限；改为泛化的「中断保留现场」，覆盖模型调用失败与 SSE 客户端断连等真实中断路径。 -->

#### Scenario: 一轮对话完整留痕
- **WHEN** 用户输入问题，模型经若干次工具调用后给出回复
- **THEN** `history.jsonl` 依次包含该用户输入、各 assistant 回复（含工具调用）与各工具结果，顺序与产生顺序一致

#### Scenario: 中断保留现场
- **WHEN** 循环因模型调用失败或客户端断连而中断
- **THEN** 中断前已产生的消息均已记录在 `history.jsonl` 中，下次对话在其基础上继续

### Requirement: SessionDB 并发访问保护

SessionDB 的 session 映射 SHALL 由读写锁保护，使 SSE 模式下并发请求对映射的读写不导致进程崩溃：缓存命中的读取 SHALL 可并发进行，未命中时的写入 SHALL 互斥，且并发未命中同一 id 时 MUST 只保留一个 session 实例。对同一 session 对象的并发轮次驱动（多个请求同时运行同一 session id 的循环）MUST NOT 被视为本版支持的行为——本版由客户端按 session id 串行化请求保证安全，细粒度的同会话并发留待未来。

#### Scenario: 不同 session 的并发请求不崩溃
- **WHEN** 两个携带不同 session id 的请求并发到达，各自触发映射的读取与写入
- **THEN** 映射访问经读写锁保护，进程不因并发 map 读写而崩溃，两请求各自得到正确的 session 对象

#### Scenario: 并发未命中同一 id 只保留一个实例
- **WHEN** 两个携带同一未缓存 session id 的请求并发到达
- **THEN** 二者最终共享同一个 session 实例，映射中该 id 只对应一个对象

## MODIFIED Requirements

### Requirement: Session 初始化与加载

agent SHALL 维护全局 SessionDB（session id 到 session 对象的映射）。交互与 one-shot 模式在启动时确定单一 session id 并加载它；SSE 模式 SHALL 按每个请求携带的 session id 惰性加载对应 session（缓存命中复用内存对象，未命中从磁盘重放）。任一模式下加载一个 session 时，其 `history.jsonl` 存在 SHALL 按行序逐条反序列化为该 session 的历史消息，不存在 SHALL 以该 id 新建空 session。系统 MUST NOT 扫描 `.laxcode/.session/` 目录批量加载未被启动或请求引用的 session（目录扫描式加载留待未来优化）。

#### Scenario: 加载已存在的 session 恢复历史
- **WHEN** 指定的 session id 在 `.laxcode/.session/` 下存在 `history.jsonl`
- **THEN** 该文件中的消息按行序恢复为该 session 的历史，后续对话在其基础上继续

#### Scenario: 指定不存在的 session id 新建空会话
- **WHEN** 指定的 session id 无对应 `history.jsonl`
- **THEN** 以该 id 建立空历史的 session，会话正常开始

#### Scenario: SSE 模式按请求惰性加载多个 session
- **WHEN** SSE 模式下先后到达携带不同 session id 的请求
- **THEN** 每个被请求的 session 各自按需加载并缓存，未被任何请求引用的 session 目录不读取、不进入内存

#### Scenario: 未指定的其他 session 不被加载
- **WHEN** `.laxcode/.session/` 下存在多个 session 目录，但只有部分 id 被启动或请求引用
- **THEN** 除被引用 id 外的其他 session 历史不读取、不进入内存

### Requirement: Session id 来源

交互与 one-shot 模式下 session id SHALL 通过命令行参数指定；SSE 模式下 SHALL 由每个 HTTP 请求体携带。任一模式下未提供 session id 时 SHALL 以当前时间串生成新 id，时间串精度 MUST 达到毫秒级以避免同秒冲突。SSE 模式下服务端实际使用的 session id SHALL 经流式事件回吐给客户端，使自动生成的 id 对客户端可见。

#### Scenario: 命令行指定 session id
- **WHEN** 启动命令携带 session id 参数
- **THEN** 该 id 用于定位或新建 session

#### Scenario: SSE 请求体携带 session id
- **WHEN** SSE 模式下请求体提供了 session id
- **THEN** 该 id 用于定位或新建 session，并经流式事件回吐给客户端

#### Scenario: 未指定时生成时间串 id
- **WHEN** 启动命令或 SSE 请求未携带 session id
- **THEN** 以当前时间串（毫秒精度）生成新 id，并以此新建 session

## REMOVED Requirements

### Requirement: 会话内消息追加
**Reason**: 原需求含依赖已废弃 turn 上限的「工具循环超限中断保留现场」Scenario（turn 上限已在 `2026-09-02-bash-tool-bg-process-safety` 移除）。OpenSpec 不支持在保留需求名的前提下删单个 Scenario，故整条移除并以 cleaned 版「会话内消息追加与中断留痕」替代（把中断留痕泛化到错误/断连中断）。
**Migration**: 消息按序追加落盘的行为不变；「中断保留现场」语义从「turn 上限中断」泛化为「模型调用失败或客户端断连中断」。参见 ADDED 的「会话内消息追加与中断留痕」。

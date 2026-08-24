## Purpose

将 agent 的对话历史按 session 持久化到 `.laxcode/.session/<session_id>/history.jsonl`，使会话历史在进程退出后可恢复，为续聊与会话管理提供基础；同时定义历史的加载、追加、视图组装与容错行为。

## ADDED Requirements

### Requirement: Session 历史持久化格式

每个 session 的对话历史 SHALL 持久化于 `<工作目录>/.laxcode/.session/<session_id>/history.jsonl`，文件中每一行 SHALL 是一条 JSON 序列化的消息对象（含角色、内容、工具调用等字段）。文件 SHALL 只记录 user 输入、assistant 回复与工具结果消息；system prompt MUST NOT 写入该文件。

#### Scenario: 消息以单行 JSON 追加
- **WHEN** 会话中产生一条新消息（用户输入、assistant 回复或工具结果）
- **THEN** `history.jsonl` 追加恰好一行该消息的 JSON 序列化结果

#### Scenario: system prompt 不落盘
- **WHEN** 会话启动并构建 system prompt
- **THEN** `history.jsonl` 中不出现 system 角色的消息记录

### Requirement: Session 初始化与加载

agent 启动时 SHALL 初始化全局 SessionDB（session id 到 session 对象的映射），并仅加载启动时确定的那一个 session：其 `history.jsonl` 存在时，SHALL 按行序逐条反序列化为该 session 的历史消息；不存在时，SHALL 以该 id 新建空 session。本版 MUST NOT 扫描或加载其他 session 的历史（加载机制留待未来优化）。

#### Scenario: 加载已存在的 session 恢复历史
- **WHEN** 启动时指定的 session id 在 `.laxcode/.session/` 下存在 `history.jsonl`
- **THEN** 该文件中的消息按行序恢复为该 session 的历史，后续对话在其基础上继续

#### Scenario: 指定不存在的 session id 新建空会话
- **WHEN** 启动时指定的 session id 无对应 `history.jsonl`
- **THEN** 以该 id 建立空历史的 session，会话正常开始

#### Scenario: 未指定的其他 session 不被加载
- **WHEN** `.laxcode/.session/` 下存在多个 session 目录，但启动时只确定了一个 session id
- **THEN** 除该 id 外的其他 session 历史不读取、不进入内存

### Requirement: 历史加载容错

加载 `history.jsonl` 时，无法反序列化的行与空白行 SHALL 被跳过并输出警告，MUST NOT 阻断 agent 启动，其余行正常加载。

#### Scenario: 残缺 JSON 行跳过
- **WHEN** `history.jsonl` 末行是进程中断留下的半个 JSON 对象
- **THEN** 该行被跳过并输出警告，之前的完整行正常恢复，agent 正常启动

#### Scenario: 空白行跳过
- **WHEN** `history.jsonl` 中存在空行
- **THEN** 空行被跳过，不产生消息、不报错

### Requirement: 会话内消息追加

会话运行期间产生的每条消息——用户输入、assistant 回复（含工具调用）、工具执行结果——SHALL 以产生顺序追加到所属 session 的历史并落盘到 `history.jsonl`。单轮工具循环达到上限而中断时，已产生的消息 SHALL 保留在历史中。

#### Scenario: 一轮对话完整留痕
- **WHEN** 用户输入问题，模型经若干次工具调用后给出回复
- **THEN** `history.jsonl` 依次包含该用户输入、各 assistant 回复（含工具调用）与各工具结果，顺序与产生顺序一致

#### Scenario: 工具循环超限中断保留现场
- **WHEN** 单轮工具循环达到轮次上限而中断
- **THEN** 中断前已产生的消息均已记录在 `history.jsonl` 中，下一轮对话在其基础上继续

### Requirement: 历史视图组装

发送给大模型的历史 SHALL 由启动时构建的 system prompt 与 session 当前历史组装而成（system prompt 在头部）；system prompt SHALL 每次启动重新构建（含最新技能索引），不从持久化数据读取。

#### Scenario: 视图头部为重建的 system prompt
- **WHEN** session 历史非空，向大模型发起请求
- **THEN** 请求历史的头部是本次启动重建的 system prompt，其后为 session 已有历史与本轮新消息

#### Scenario: 续聊时 prompt 升级生效
- **WHEN** 恢复一个旧 session，且期间 system prompt 模板或技能索引发生了变化
- **THEN** 该会话使用的 system prompt 为当前重建版本，而非创建该 session 时的版本

### Requirement: 历史写盘失败容错

历史写盘失败时 SHALL 输出显眼警告，agent 会话 MUST NOT 因此中断，后续消息继续尝试落盘。

#### Scenario: 写盘失败不中断会话
- **WHEN** 追加消息时 `history.jsonl` 写入失败（如磁盘故障或权限问题）
- **THEN** 输出显眼警告，当前对话继续进行，agent 不退出

### Requirement: Session id 来源

session id SHALL 通过命令行参数指定；未指定时 SHALL 以当前时间串生成新 id，时间串精度 MUST 达到毫秒级以避免同秒启动冲突。

#### Scenario: 命令行指定 session id
- **WHEN** 启动命令携带 session id 参数
- **THEN** 该 id 用于定位或新建 session

#### Scenario: 未指定时生成时间串 id
- **WHEN** 启动命令未携带 session id 参数
- **THEN** 以当前时间串（毫秒精度）生成新 id，并以此新建 session

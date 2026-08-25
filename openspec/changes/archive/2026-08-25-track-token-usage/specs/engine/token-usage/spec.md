# Delta Spec: engine/token-usage

## Purpose

记录并暴露 agent 会话的 token 消耗：以 raw 口径（单次调用的完整计费值）回填 assistant 消息的 usage，由会话层维护"累计消耗"与"上下文窗口占用"两个统计口径，并以 meta.json 快照持久化供用户快查。

## ADDED Requirements

### Requirement: assistant 消息回填 raw token 用量

每次成功调用大模型后，本次调用产生的 assistant 消息 SHALL 记录该次调用的完整用量：token 输入数 SHALL 为本次请求发送的全部输入的 token 数（含 system prompt 与当时全部会话历史），token 输出数 SHALL 为本次响应输出的 token 数。user、工具结果与 system 消息 MUST NOT 记录非零用量（保持零值）。调用失败时无消息产生，SHALL 不记录任何用量。

#### Scenario: 成功调用回填完整用量

- **WHEN** 一次成功的大模型调用发生，请求输入共 70,000 token、响应输出 5,000 token
- **THEN** 产生的 assistant 消息记录 token 输入 70,000、token 输出 5,000

#### Scenario: 用量随消息持久化

- **WHEN** 一条记录了用量的 assistant 消息被追加进会话历史
- **THEN** 会话历史持久化文件中该消息的 JSON 行包含其用量字段

#### Scenario: user 与工具结果消息不携带用量

- **WHEN** 用户输入或工具执行结果被追加进会话历史
- **THEN** 这些消息的用量字段为零值

### Requirement: 会话累计消耗统计

会话 SHALL 维护一个累计 token 消耗统计，其值 SHALL 始终等于会话历史中全部消息用量字段的总和。由于仅 assistant 消息携带非零用量，该统计的语义为：本会话自记账启用以来每次大模型调用的计费 token 累加（输入侧每次调用按当时完整上下文重复计入，与真实计费一致）。统计 MUST 只经由追加消息的路径更新，MUST NOT 存在绕过消息历史的独立更新途径。

#### Scenario: 追加带用量的消息后统计同步

- **WHEN** 会话累计统计为输入 50,000、输出 4,000，追加一条用量为输入 70,000、输出 5,000 的 assistant 消息
- **THEN** 会话累计统计变为输入 120,000、输出 9,000

#### Scenario: 追加零用量消息不影响统计

- **WHEN** 追加一条用户输入或工具结果消息
- **THEN** 会话累计统计保持不变

### Requirement: 上下文窗口占用统计

会话 SHALL 维护一个上下文窗口占用统计，其值 SHALL 为最后一条携带非零用量的 assistant 消息的用量原值：token 输入与 token 输出之和近似等于"system prompt 加全部历史消息"下一次请求将占用的上下文窗口大小。该统计 SHALL 仅在追加携带非零用量的 assistant 消息时刷新。追加 user 或工具结果消息后、下一次调用发生前，真实窗口占用大于该统计值（新消息自身 token 数本地不可知），SHALL 视为该统计的固有滞后而非缺陷。

#### Scenario: assistant 消息刷新窗口统计

- **WHEN** 追加一条用量为输入 70,000、输出 5,000 的 assistant 消息
- **THEN** 窗口占用统计刷新为输入 70,000、输出 5,000，两者之和近似当前上下文总占用

#### Scenario: 其后追加 user 消息不刷新统计

- **WHEN** 窗口占用统计刷新后，用户输入一条新消息
- **THEN** 窗口占用统计保持原值，直到下一次成功调用的 assistant 消息刷新它

### Requirement: meta.json 快照持久化

每个 session 目录下 SHALL 维护一个 `meta.json` 文件，存放累计消耗统计与上下文窗口占用统计供用户直接查看。文件 SHALL 为顶层 JSON 对象并包含版本号字段以保留格式扩展性。文件 SHALL 在任一统计值变化时整体重写，重写 SHALL 以原子方式完成（临时文件加重命名），MUST NOT 以追加方式写入。写失败时 SHALL 输出警告但 MUST NOT 中断会话。

#### Scenario: 统计变化触发快照更新

- **WHEN** 一次成功调用使累计消耗与窗口占用统计发生变化
- **THEN** `meta.json` 被整体重写为新值，文件内容可被用户直接读取查看两项统计

#### Scenario: 快照写失败不中断会话

- **WHEN** 重写 `meta.json` 时写入失败
- **THEN** 输出警告，当前对话继续进行，agent 不退出

#### Scenario: 无消耗的会话不留快照文件

- **WHEN** 一个会话自建立以来从未发生过带用量的消息追加（如仅追加过用户输入即退出）
- **THEN** 该 session 目录下不产生 `meta.json`

### Requirement: 加载时重放恢复统计

加载 session 历史恢复会话时，两项统计 SHALL 以对历史消息的重放结果为准：累计消耗 SHALL 为全部历史消息用量字段的总和；上下文窗口占用 SHALL 为最后一条携带非零用量的消息的用量原值。`meta.json` SHALL 仅作为快查快照，其缺失、损坏、滞后或与重放结果不一致时 MUST 以重放结果为准，且 MUST NOT 阻断启动。记账启用前的旧会话历史中消息用量均为零值，恢复后累计消耗从零开始累计，窗口占用保持零值（未知态）直到下一次成功调用刷新。

#### Scenario: 重放求和恢复累计消耗

- **WHEN** 加载一个包含三条带用量 assistant 消息（输入分别为 10,000、20,000、30,000）的会话
- **THEN** 恢复后的累计消耗统计输入为 60,000

#### Scenario: 取最后一条非零消息恢复窗口占用

- **WHEN** 加载的会话中最后一条携带非零用量的消息用量为输入 30,000、输出 2,000
- **THEN** 恢复后的窗口占用统计为输入 30,000、输出 2,000

#### Scenario: meta.json 损坏时以重放为准

- **WHEN** `meta.json` 内容不是合法 JSON 或字段缺失
- **THEN** 忽略该文件，统计按历史重放恢复，agent 正常启动

#### Scenario: 旧会话从零累计

- **WHEN** 恢复一个记账启用前创建的会话，其历史消息均无用量记录
- **THEN** 恢复后的累计消耗为零值，后续新调用的用量正常累加

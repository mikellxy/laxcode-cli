## Context

见 proposal.md 的 Why。当前与设计相关的现状约束：

- `AgentEngine.contextHis []schema.Message` 纯内存持有全部历史（含头部 system prompt），`Run` 通过"复制 slice 头 + defer 回写"维护累积结果（[main_loop.go:81-86](../../../internal/engine/main_loop.go)），存在共享底层数组的心智负担。
- provider 层只消费 `[]schema.Message`，按 Role 分发转换，对历史来源无感——替换历史容器不动 provider。
- `schema.Message` 的 json tag 完整、`ToolCall.Arguments` 为 `json.RawMessage`（round-trip 安全）、Content 换行被 JSON 转义——一行一消息的 jsonl 格式天然成立。
- 项目容错哲学（见 `context/skill-index` spec）：加载类失败一律警告跳过，绝不阻断启动。
- `.laxcode/.session/` 目录已存在（空），与 `skills/` 并列。
- 现有测试 `engine_test.go` 直接操作 `e.contextHis`（包内访问），需要同步适配。

## Goals / Non-Goals

**Goals:**

- 对话历史按 session 落盘（jsonl），进程退出后可恢复
- 消灭 `contextHis` 与 Run 中的 defer 回写微操，历史有唯一真相源
- main.go 只需感知 session id，不感知 db 的存在细节

**Non-Goals:**

- 不做全量 session 扫描 / 懒加载 / session 列表与切换 UX（`-resume` 等），留待未来优化
- 不做并发安全（v1 为单 goroutine REPL）；但 Session 方法签名收口，为将来加锁不留调用方改动
- 不做历史压缩、token 预算、消息裁剪

## Decisions

### D1: system prompt 不落盘，启动时重建

`history.jsonl` 只存 user/assistant/tool 消息。system prompt 是**派生数据**（由代码版本 + 技能集合计算），不是用户对话的一部分；落盘会引入"源已变、缓存未变"的一致性问题（改了 sysprompt.go 或加了 skill，续聊仍用旧 prompt）。代价仅为一次视图组装（见 D5），换来 prompt 升级即时生效。

备选：全量落盘（文件即完整状态、load 即用）——被否，冻结 prompt 的代价大于组装的复杂度。

### D2: v1 只加载启动指定的一个 session

main.go 手中只有一个 session id，启动时全量扫描 `.laxcode/.session/` 建映射在 v1 是 dead path（加载的旧 session 永远不会被用到）。故 `SessionDB` 在 v1 退化为：初始化时加载指定的单个 session 入 map，`Loop` 按 id 纯查询。

备选：全量 load（为 session 列表/切换铺路，但当下零收益）/ 懒加载（终局形态，区分"未加载"与"不存在"略复杂）——均留待未来按需演进；本版接口按"db 持有映射、Loop 只查"设计，演进时只改初始化函数内部。

### D3: 每条消息即写（O_APPEND 追加一行）

`Append` = 内存 append + 追加写一行 JSON。REPL 消息频率低，无性能顾虑；换来最强现场保留——进程任意时刻崩溃最多丢"正在写的那条"，且工具循环超限（errTooManyTurns）中断时部分对话自动留痕，语义与现有 defer 回写一致。

备选：Run 结束批量 flush——写放大低但中途崩溃丢整轮，被否。

### D4: 写盘失败 → 显眼警告，REPL 继续

对齐 skill 的"不阻断"哲学，但区别于 skill（加载失败是降级），历史写失败是**数据丢失**，警告必须显眼（用户需知道对话可能未保存）。不 fail fast 的理由：交互现场优先，磁盘故障不应蒸发用户正在进行的工作。

### D5: `View(sys)` 每轮重拼，消灭 defer 回写

`View` 返回 `[system] + Messages` 的**新切片**；Run 的工具循环每次调 `provider.Generate` 前现拼视图。历史唯一真相源是 Session，视图是廉价重拼（消息数几百以内一次 slice 拷贝可忽略）。现有 `Run` 的"复制 slice 头 + defer 回写"（共享底层数组的 aliasing 处理）整体删除。

配套纪律：Run 中所有消息追加 MUST 走 `Session.Append`，禁止绕过直接改 `Messages`——通过将该字段设为非导出并只经方法访问来强制。

### D6: SessionDB 为 engine 包级全局（非导出 + Init 函数收口）

参照 `env.WorkDir` 的包级变量先例，但 Session/SessionDB 定义在 engine 包（放 env 会造成底层包依赖上层的倒挂）。形态：

- 非导出包级变量 `sessionDB`
- `InitSessionDB(workDir, sessionID)`：main.go 启动时调用一次——建 db、读指定 id 的 `history.jsonl`（存在则逐行反序列化入 Session，不存在则空 Session）、挂到全局
- `Loop` 内部走包内查询函数按 id 取 session，全局变量对 engine 包外不可见

备选：`NewAgentEngine` 构造注入（最可测，但签名变更波及 main.go 与现有测试）/ Loop 局部变量（最简，但多入口时需上提）——被否的原因：全局形态与"main.go 初始化、Loop 消费"的用户决策一致，且将来 feishu 入口出现时上提成本极低（挪一行）。

测试策略：session 的 load/append round-trip 测试走纯函数路径（显式传临时目录构造），不碰全局；main_loop 测试改走 session 构造。

### D7: session id 时间串精确到毫秒

缺省 id 用 `20060102-150405.000` 格式（Go 时间格式串），消除同秒双开撞 id（撞 id 会把两个会话写进同一文件）。命令行参数优先。

## Risks / Trade-offs

- [进程写盘中途崩溃，末行残缺 JSON] → load 时跳过坏行并警告（D3 场景的对称处理，见 spec"历史加载容错"）
- [全局状态对测试不友好（用例间共享）] → session 纯函数路径测试；全局仅被 main.go 写一次
- [绕过 Append 直接修改历史导致内存与磁盘失同步] → Messages 非导出强制收口
- [无锁单 goroutine 假设被将来 feishu 多 goroutine 打破] → D6 的方法收口使加锁只动 Session 内部，不动调用方
- [同毫秒并行启动仍可能撞 id] → 概率可忽略；将来引入 uuid 或加随机后缀即可

## Migration Plan

无存量数据迁移：`.laxcode/.session/` 为空目录，v1 生成的文件即新格式。回滚 = 还原代码（遗留的 session 目录无消费者，无害）。

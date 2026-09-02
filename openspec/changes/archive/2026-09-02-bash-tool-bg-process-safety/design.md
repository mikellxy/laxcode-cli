## Context

现状见 proposal.md。与设计相关的代码事实：

- `internal/tools/bash.go` 用 `cmd.CombinedOutput()` 捕获输出（内部创建管道），`exec.CommandContext` + 30s 超时。
- `NewBashTool(workDir)` 在 `cmd/main/main.go:156` 与 `internal/engine/subagent.go:80` 各自实例化，随 `DefaultRegistry` 一起每次 agent 运行新建一份——工具实例与运行同生命周期。
- 运行级 ctx 来自 `context.Background()`（TerminalLoop/OneShotLoop），**永不取消**，清理钩子不能挂在 ctx 上。
- `BaseTool` / `Registry` 接口目前无生命周期方法。

## Goals / Non-Goals

**Goals:**

- 消除后台进程导致的工具挂死（可证的返回上界 = 超时时长）。
- 超时收割整棵进程树，且不误伤其他命令留下的合法后台进程。
- 后台进程跨工具调用存活，会话结束时统一回收进程与临时文件。
- 零新依赖（仅标准库），对 LLM 可见的返回格式不变。

**Non-Goals:**

- 持久 shell 会话（`cd`、环境变量跨调用保留）——未来如需再立项。
- 后台进程跨会话存活、Windows 支持、daemonizing 进程（double-fork/setsid 越狱）的防护。
- 流式输出（工具一次性返回快照，现状亦如此）。

## Decisions

### D1: 临时文件替代管道捕获输出

每次 `Execute` 用 `os.CreateTemp` 创建独立文件，`cmd.Stdout` 与 `cmd.Stderr` 指向同一 `*os.File`（Go 对同一文件复用 fd，共享偏移量，天然保留 `2>&1` 合并语义）。`cmd.Run()` 返回后 `os.ReadFile` 读取内容。

- 为何不用 `WaitDelay`：进程退出后强制关管道，后台进程后续写 stdout 会收到 SIGPIPE/BrokenPipeError，把本应存活到会话末的服务器打挂——与核心用例直接冲突。临时文件让后台进程持有的是普通文件 fd，写多久都安全。
- 为何不用持久 shell 会话：解决挂死之余还引入会话状态管理，改动面大一个量级；当前痛点不需要。
- 每次 Execute 独立 temp 文件：并发工具调用天然不串流。
- 快照语义：返回内容 = `Run()` 返回时刻的文件内容。后台进程块缓冲（非 tty）意味着快照通常只含前台命令输出。

### D2: per-command 进程组 + 超时组杀

`cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`（pgid 即 `cmd.Process.Pid`）。`exec.CommandContext` 的默认 Kill 只杀直接子进程，故需自建 goroutine 监听 `ctx.Done()` 后执行 `syscall.Kill(-pgid, SIGKILL)`，确保孙进程一并收割。

- 为何不用单一会话级进程组：任一命令超时会连带杀掉所有先前留下的后台服务器，违背"后台任务不受其他命令命运牵连"的语义。per-command 组把爆炸半径限定在失控命令自身的进程树内。

### D3: 注册表放 BashTool 实例，经 Registry.Close() 触发清理

BashTool 新增字段记录本次运行所有调用的 `(pgid, tempfilePath)`。清理入口采用可选接口：

```go
type closer interface{ Close() error }
```

`DefaultRegistry` 增加 `Close()`，遍历已注册工具，对实现了 `closer` 的调用之。调用点：

- `TerminalLoop` / `OneShotLoop` 退出时 defer 调用（覆盖正常返回与错误返回）。
- `subagent.go` 创建 registry 处 defer 调用（子 Agent 运行即一次完整生命周期）。

不做 ctx 挂钩（`context.AfterFunc`）的原因：运行级 ctx 是 Background 永不取消，信号不可靠。`Close()` 中对每个记录执行 `Kill(-pgid, SIGKILL)`（忽略"进程不存在"错误）并删除临时文件；注册表需并发安全（mutex），Execute 并发时写入。

### D4: 工具描述引导（S4）

description 中加入后台任务标准写法示例：

```
python3 server.py > /tmp/srv.log 2>&1 & echo "pid=$!"
```

理由：非交互 bash 不打印 job 信息，LLM 否则拿不到 pid；重定向让后台日志与前台输出分流，后续可 `tail` 排错。这是提示层引导，非强制约束。

## Risks / Trade-offs

- [快照语义：后台进程的早期输出可能缺失或迟到] → S4 引导后台进程显式重定向到自有日志文件，返回流天然分离；快照缺失的只是"未显式重定向的后台日志"。
- [临时文件在会话期间无限增长（话痨服务器持续写）] → 会话末 Close() 删除；返回内容本就有 8000 rune 截断。接受会话期间的磁盘占用，不做限额。
- [pgid 复用误伤：长会话中已死组号被无关进程复用，扫尾误杀] → 概率低（pid 空间大、会话有限长）；如未来需要，Linux 上可用 pidfd 固定身份。暂接受。
- [Go 进程崩溃时 Close() 不执行，泄漏照旧] → 现状更糟（永久挂死）；不做崩溃级兜底（supervisor 进程复杂度不成比例）。
- [Setpgid + Kill(-pgid) 依赖 POSIX，无 Windows] → 现状亦不支持 Windows，非回归。
- [交互式终端 Ctrl-C 直接杀进程时 defer 不执行] → 见 Open Questions。

## Migration Plan

纯内部行为改造：`ExecResult` 结构与返回格式不变，LLM 无感知（仅 description 文案更新）。回滚 = revert 单 commit。无数据迁移、无 API 兼容层。

## Open Questions

- TerminalLoop 的 Ctrl-C 退出路径是否需要 signal handler 保障 `Registry.Close()` 执行？（defer 覆盖正常返回；信号直杀进程时 defer 不跑。）可在实现时确认 TerminalLoop 现有的信号处理方式再定，不阻塞主体方案。

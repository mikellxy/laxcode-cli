# Proposal: bash-tool-bg-process-safety

## Why

LLM 通过 bash 工具启动后台进程（如 `python3 run_server.py & curl ...`）测试后、再用 `kill` 清理的常见工作流，会让 `internal/tools/bash.go` 的 `cmd.CombinedOutput()` 永久阻塞：后台进程继承管道写端导致 `Wait()` 等不到 EOF，而 30s 超时只会 kill 直接子进程 bash，管道依旧被持有——工具调用无限挂死，agent 主循环彻底停摆。

## What Changes

- **输出改为临时文件重定向（S3）**：`cmd.Stdout`/`cmd.Stderr` 指向 `os.CreateTemp` 创建的同一文件（保留 stdout+stderr 合并语义），`Run()` 后读取文件内容作为输出。彻底消除管道写端被后台进程持有导致的 `Wait()` 阻塞。
- **进程组隔离与超时收割（S2）**：每次 Execute 以 `Setpgid` 建立独立进程组；超时（或 ctx 取消）时 `Kill(-pgid, SIGKILL)` 收割整棵进程树，而非只杀 bash 留下孤儿后台进程。
- **工具描述引导（S4）**：bash 工具 description 增加后台任务的标准姿势——`python3 server.py > /tmp/srv.log 2>&1 & echo "pid=$!"`，让 LLM 输出流分离、拿到准确 pid 用于后续 kill。
- **会话末清理**：BashTool 维护 (pgid, tempfile) 注册表，随会话生命周期结束时统一 `Kill(-pgid)` 并删除临时文件，兜底 LLM 忘记 kill 的后台进程和无限增长的日志文件。

## Capabilities

### New Capabilities

- `tools/bash-execution`: bash 工具的执行语义——命令执行、输出捕获（stdout/stderr 合并到临时文件）、超时行为（进程组收割）、后台进程生命周期（会话末统一回收）、输出截断规则。

### Modified Capabilities

（无——`tools/write-file`、`tools/edit-file` 等现有 spec 不受影响）

## Impact

- **代码**：
  - `internal/tools/bash.go`：核心改造点（Execute 重写输出捕获与进程管理、新增注册表、更新 Definition 的 description）。
  - `internal/engine/` 或会话生命周期所属模块：新增会话结束时触发 BashTool 清理的钩子（具体挂载点在 design 阶段确定）。
- **行为变化**：
  - 后台进程的输出不再进入工具返回（快照语义）；返回内容 = Wait 返回时刻的文件内容。
  - 超时行为从"kill bash + 工具可能永久挂死"变为"收割整个进程组后必然返回"。
  - 后台进程在会话结束后不再存活。
- **依赖**：仅标准库（`syscall.SysProcAttr`、`os.CreateTemp`）；macOS/Linux 均支持 `Setpgid`，无 Windows 兼容计划（现状亦不支持）。

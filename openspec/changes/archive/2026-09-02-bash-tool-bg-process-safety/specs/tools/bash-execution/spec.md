## Purpose

定义 bash 工具的执行语义：命令如何执行、输出如何捕获与截断、超时如何收割进程树、后台进程如何跨工具调用存活并在会话结束时统一回收。

## ADDED Requirements

### Requirement: 命令执行与合并输出

bash 工具 SHALL 在指定工作目录执行 LLM 提供的 bash 命令，并将该命令的 stdout 与 stderr 合并捕获后返回。返回内容包含退出码与输出文本；输出超过 8000 字符（按 rune 计）时 SHALL 截断并在描述中注明截断。

#### Scenario: 普通命令返回合并输出

- **WHEN** LLM 调用 bash 工具执行 `echo out; echo err 1>&2`
- **THEN** 工具返回 exit_code=0，输出中同时包含 stdout 内容 `out` 与 stderr 内容 `err`

#### Scenario: 命令非零退出

- **WHEN** 执行的命令以非零码退出（如 `false`）
- **THEN** 工具正常返回（非 error），结果中携带对应退出码与失败描述

#### Scenario: 输出超长截断

- **WHEN** 命令输出超过 8000 字符
- **THEN** 返回内容截断至前 8000 字符并标记 truncated=true

### Requirement: 后台进程不得阻塞工具返回

当命令启动后台进程（如 `cmd &`）而主命令已退出时，工具 SHALL 在主命令退出后即返回，不得等待后台进程结束。返回内容为返回时刻已写出的输出快照；后台进程 SHALL 继续存活，可被后续工具调用通过 pid 终止。

#### Scenario: 后台启动服务器并测试

- **WHEN** LLM 执行 `python3 run_server.py & curl -s localhost:8000/health`
- **THEN** 工具在 curl 结束后（而非服务器退出后）返回，输出包含 curl 的响应；服务器进程继续运行

#### Scenario: 后台进程不因工具返回而死亡

- **WHEN** LLM 执行 `sleep 1000 &` 后工具返回
- **THEN** sleep 进程在工具返回后仍然存活，后续调用 `kill -9 <pid>` 可将其终止

### Requirement: 超时收割整个进程树

命令执行超过 30 秒（或调用方 context 取消）时，工具 SHALL 终止该命令进程组内的全部进程（含其派生的后台进程）并在超时上限附近返回超时错误，不得出现无限期挂起。进程组回收 SHALL 仅限超时命令自身派生的进程，不得波及此前命令留下的、仍正常存活的后台进程。

#### Scenario: 前台命令超时

- **WHEN** LLM 执行 `sleep 1000`（无后台）
- **THEN** 约 30 秒后工具返回超时错误，sleep 进程不再存活

#### Scenario: 超时收割连同后台进程

- **WHEN** LLM 执行 `sleep 1000 & sleep 1000`（前台挂起且派生后台进程）
- **THEN** 超时后两个 sleep 进程均被终止，工具返回超时错误

#### Scenario: 不误伤先前留下的后台进程

- **WHEN** LLM 先执行 `sleep 1000 &`（正常返回，后台存活），再执行一条会超时的挂起命令
- **THEN** 超时回收后，第一条命令留下的 sleep 进程仍然存活

### Requirement: 会话结束回收后台进程与临时文件

agent 运行结束时，本次运行内由 bash 工具启动的、仍存活的后台进程 SHALL 被统一终止，输出捕获所用的临时文件 SHALL 被删除。该回收不依赖 LLM 主动执行 kill。

#### Scenario: LLM 遗忘清理的后台进程被回收

- **WHEN** LLM 启动后台服务器后未执行任何 kill，agent 运行正常结束
- **THEN** 该服务器进程被终止，不再占用端口

#### Scenario: 临时文件删除

- **WHEN** agent 运行结束
- **THEN** 本次运行所有 bash 工具调用创建的输出临时文件已不存在

### Requirement: 后台任务用法引导

bash 工具的 description SHALL 引导 LLM 采用后台任务标准写法：后台进程的输出重定向到独立日志文件（如 `> /tmp/srv.log 2>&1`），并用 `echo "pid=$!"` 将 pid 打印到输出中，供后续 kill 使用。

#### Scenario: LLM 依引导获得可 kill 的 pid

- **WHEN** LLM 按 description 引导执行 `python3 server.py > /tmp/srv.log 2>&1 & echo "pid=$!"`
- **THEN** 工具返回中出现 `pid=<数字>`，LLM 可在后续调用中以该 pid 终止服务器

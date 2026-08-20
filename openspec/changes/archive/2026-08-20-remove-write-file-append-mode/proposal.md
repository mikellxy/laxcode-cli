## Why

`write_file` 的 `append` 模式让写入结果依赖文件的当前状态（非幂等）：当模型对文件内容的认知与磁盘真实内容发生漂移时，追加会静默产出错位内容并让任务悄悄失败。同时 append 本质上是增量编辑，属于规划中 `edit_file` 工具的职责范围，留在 `write_file` 中造成两个工具语义重叠。收窄后 `write_file` 只做一件事：声明文件终态。

## What Changes

- **BREAKING**（对工具调用方 LLM）：移除 `write_file` 的 `mode` 参数——工具定义（schema）中不再声明，执行逻辑中不再解析，不再支持追加写
- 成功返回值从 `file written: <相对路径> (mode=write)` 改为 `内容成功写入文件：<相对路径>`
- 工具 description 收窄为「写入完整文件内容，创建新文件或覆写已有文件」
- 新增持久化单元测试 `internal/tools/write_file_test.go`，覆盖创建、覆写、路径穿越拦截、绝对路径拦截等场景；测试使用临时目录且不留残余文件

## Capabilities

### New Capabilities
- `tools/write-file`: `write_file` 工具的行为规范——全量写入语义（创建/覆写）、工作目录路径安全约束、成功返回值格式

### Modified Capabilities
<!-- openspec/specs/ 目前为空，无既有 capability 被修改 -->

## Impact

- `internal/tools/base.go`：`WriteFileTool` 的 `Definition()`（移除 mode 属性、更新 description）与 `Execute()`（移除 mode 解析与 append 分支、更新返回值）
- `internal/tools/write_file_test.go`：新增测试文件
- 不受影响：`registry.go`（注册方式不变）、`sysprompt.go`（未提及 write_file 用法）、engine/provider 各层（均不感知 mode）
- `docs/self-reinforcement/dev_write_file_tool.md` 保持原样——历史开发记录不篡改，本次决策理由沉淀在本 change 的 design.md
- `edit_file` 工具不在本次范围内，另行立项

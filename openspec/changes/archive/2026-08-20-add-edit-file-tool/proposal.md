## Why

移除 write_file 的 append 模式时，增量编辑职责被明确移交给未来的 edit 工具。当前 LLM 修改文件的唯一途径是 write_file 全量覆写：大文件场景下 token 开销高，且模型重新生成全文容易引入无意识改动。需要 edit_file 工具让模型只提供旧片段与新片段即可完成局部修改。

## What Changes

- 新增 `edit_file` 工具（`EditFileTool`）：必选参数 `path`（工作目录内相对路径）、`old_text`、`new_text`；读取文件、将匹配到的 `old_text` 替换为 `new_text` 后回写
- 匹配采用四级宽容降级策略，抵御模型返回的 `old_text` 格式偏差：精确匹配 → 换行符归一化匹配 → 忽略 `old_text` 首尾空白匹配 → 逐行去空白滑动窗口匹配
- 任一层匹配到多处即报错，返回各处行号，要求模型扩大 `old_text` 上下文使其唯一
- 注册进 `DefaultRegistry`，与 read_file / write_file / bash 并列

## Capabilities

### New Capabilities

- `tools/edit-file`: edit_file 工具的行为契约——参数校验、路径安全、四级匹配降级、唯一性约束、成功与失败反馈格式、换行符统一 LF 的输出语义

### Modified Capabilities

（无——write_file 行为不变，本变更只新增能力）

## Impact

- `internal/tools/base.go`: 新增 `EditFileTool` 及其四级匹配逻辑，复用 `safeJoinWorkDir` 路径防护
- `internal/tools/registry.go`: `NewDefaultRegistry` 注册 `edit_file`
- `internal/tools/edit_file_test.go`: 新增测试文件，匹配算法表驱动测试 + Execute 集成测试
- 无外部依赖变化，无 BREAKING

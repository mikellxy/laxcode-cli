## 1. 四级匹配引擎（纯函数，与文件 IO 解耦）

- [x] 1.1 实现 L1 精确匹配：在原始文件内容中定位 `old_text` 的全部出现处，收集行号；唯一命中时以 LF 归一化后的 `new_text` 在原始字节域替换
- [x] 1.2 实现 L2 换行符归一化匹配：文件内容与 `old_text` 的 `\r\n` 归一化为 `\n` 后定位；唯一命中时在归一化域替换，全文件以 LF 写回语义（返回归一化新内容）
- [x] 1.3 实现 L3 首尾空白容忍匹配：`old_text` 去除首尾空白后在归一化内容中定位，命中区间为去除空白后内容的出现区间，命中处两侧文件原有空白保留
- [x] 1.4 实现 L4 行级匹配（独立子函数）：双侧归一化后按行切分并逐行去除首尾空白，以 `old_text` 行数为窗口滑动比较；唯一命中时将命中连续行的区间整体替换为 `new_text` 原样内容
- [x] 1.5 实现统一降级入口（独立函数）：按 L1→L4 顺序尝试；任一层命中 ≥2 处返回含命中处数与行号清单的错误（文案见 spec 匹配唯一性约束）；四层均未命中返回引导重新 read_file 的错误；唯一命中返回新内容、替换行号范围、命中层级名

## 2. 匹配引擎表驱动测试

- [x] 2.1 四层各编写命中用例：唯一命中且替换结果正确（含 L2 CRLF 文件全文件 LF、L1 纯 LF 文件其余字节不变、L3 两侧空白保留、L4 缩进以 new_text 为准）
- [x] 2.2 编写失败与边界用例：每层多处命中报错且行号正确、old_text 空白拒绝、new_text 空串删除语义、全未命中错误文案

## 3. EditFileTool 封装与注册

- [x] 3.1 在 `internal/tools/base.go` 实现 `EditFileTool`：`Name`/`Definition`/`Info`/`Execute`；参数校验（path/old_text/new_text 必选，old_text 空白拒绝，new_text 允许空串）；复用 `safeJoinWorkDir`；文件不存在时报错并指路 write_file；成功返回相对路径、行号范围与命中层级
- [x] 3.2 在 `internal/tools/registry.go` 的 `NewDefaultRegistry` 注册 `edit_file`

## 4. 集成验证

- [x] 4.1 Execute 集成测试：路径穿越与绝对路径拒绝、目标文件不存在、落盘替换成功（tempdir + `env.WorkDir` 指向）
- [x] 4.2 `go build ./...` 与 `go test ./internal/tools/` 全部通过

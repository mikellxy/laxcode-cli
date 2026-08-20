## Purpose

定义 write_file 工具的全量写入行为契约：以传入内容声明文件终态（创建或覆写），约束写入目标必须位于工作目录内，并规定成功返回格式。

## ADDED Requirements

### Requirement: 全量写入语义

write_file 工具 SHALL 以传入的 `content` 作为文件的完整终态内容执行写入：目标文件不存在时创建新文件，已存在时整体覆写，旧内容不残留。父目录不存在时 SHALL 自动创建。

#### Scenario: 创建新文件并自动创建父目录
- **WHEN** 调用 write_file，`path` 为 `a/b/c.txt`（父目录不存在），`content` 为 `hello`
- **THEN** 在工作目录下创建 `a/b/c.txt`，内容为 `hello`

#### Scenario: 覆写已有文件
- **WHEN** 调用 write_file，`path` 指向已存在且内容为 `old` 的文件，`content` 为 `new`
- **THEN** 该文件内容变为 `new`，旧内容 `old` 完全消失

### Requirement: 参数契约

write_file 的工具定义 SHALL 仅声明必填参数 `path`（工作目录相对路径）与 `content`（完整文件内容）。缺少必填参数时 SHALL 返回错误且不执行写入。

#### Scenario: 缺少必填参数报错
- **WHEN** 调用 write_file 时缺少 `path` 或 `content`
- **THEN** 返回错误，未产生任何文件写入

### Requirement: 工作目录路径安全

write_file SHALL 仅接受位于工作目录内的相对路径。绝对路径 SHALL 被拒绝；经归一化后越出工作目录的相对路径（如 `../` 穿越）SHALL 被拒绝。两者均返回错误且不产生任何文件写入。

#### Scenario: 绝对路径被拒绝
- **WHEN** 调用 write_file，`path` 为绝对路径如 `/tmp/evil.txt`
- **THEN** 返回错误，`/tmp/evil.txt` 未被创建或修改

#### Scenario: 路径穿越被拒绝
- **WHEN** 调用 write_file，`path` 为 `../../evil.txt`
- **THEN** 返回错误，工作目录之外的任何文件未被创建或修改

### Requirement: 成功返回格式

写入成功时，write_file SHALL 返回「内容成功写入文件：<相对工作目录的路径>」格式的确认信息，不携带写入模式等冗余字段。

#### Scenario: 成功写入的返回值
- **WHEN** 调用 write_file 成功写入相对路径 `cmd/main/main.go`
- **THEN** 工具返回 `内容成功写入文件：cmd/main/main.go`

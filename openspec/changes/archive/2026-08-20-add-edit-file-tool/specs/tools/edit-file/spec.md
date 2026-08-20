## Purpose

edit_file 工具让调用方（LLM）通过提供旧片段与新片段对工作目录内的已有文件做局部替换修改，以四级宽容匹配抵御模型返回的 old_text 格式偏差，并以面向模型的反馈驱动其自我修正。

## ADDED Requirements

### Requirement: 参数契约

edit_file 工具 SHALL 接受必选参数 `path`（工作目录内相对路径）、`old_text`、`new_text`。`new_text` 允许为空字符串，语义为删除匹配片段。`old_text` 为空或去除空白后为空时 SHALL 拒绝执行。

#### Scenario: new_text 为空串执行删除
- **WHEN** 提供唯一的 `old_text` 且 `new_text` 为空字符串
- **THEN** 匹配片段被删除，删除后文件其余内容不变

#### Scenario: old_text 为空白被拒绝
- **WHEN** `old_text` 缺失、为空串或去除空白后为空
- **THEN** 工具报错，不做任何文件修改

#### Scenario: 目标文件不存在
- **WHEN** `path` 指向的文件不存在
- **THEN** 工具报错并提示该文件不存在、新建文件应使用 write_file

### Requirement: 路径安全

edit_file 工具 SHALL 与 write_file 遵循相同的路径约束：拒绝绝对路径，拒绝经 `../` 或其他形式逃逸出工作目录的相对路径。

#### Scenario: 绝对路径被拒绝
- **WHEN** `path` 为绝对路径
- **THEN** 工具报错，要求提供工作目录内的相对路径

#### Scenario: 路径穿越被拒绝
- **WHEN** `path` 经 `../` 解析后位于工作目录之外
- **THEN** 工具报错，不做任何文件访问

### Requirement: 四级宽容匹配降级

edit_file 工具 SHALL 按以下顺序尝试定位 `old_text`，任一层唯一命中即停止并执行替换：

1. 精确匹配：`old_text` 在原始文件内容中逐字节出现
2. 换行符归一化匹配：文件内容与 `old_text` 的 `\r\n` 均归一化为 `\n` 后匹配
3. 首尾空白容忍匹配：`old_text` 去除首尾空白后，在归一化文件内容中匹配；命中区间为去除空白后内容的出现区间，文件中命中处两侧的原有空白保持不变
4. 行级匹配：归一化文件内容与 `old_text` 各自按行切分并去除每行首尾空白后，以 `old_text` 行数为窗口大小滑动比较；命中时该连续行区间整体替换为 `new_text`，替换后缩进完全以 `new_text` 为准

#### Scenario: 精确匹配命中
- **WHEN** `old_text` 与文件内容逐字节一致且仅出现一处
- **THEN** 该处被替换为 `new_text`，文件其余字节保持不变

#### Scenario: 换行符差异经归一化命中
- **WHEN** 文件内容为 CRLF 换行而 `old_text` 为 LF 换行，归一化后仅出现一处
- **THEN** 匹配命中并完成替换，整个文件以 LF 换行写回

#### Scenario: old_text 首尾多余空白被容忍
- **WHEN** `old_text` 首尾带有文件中不存在的空白（如代码块缩进或空行），去除首尾空白后在归一化内容中仅出现一处
- **THEN** 命中区间为去除空白后内容的出现位置并完成替换，命中处两侧文件原有的空白（如行尾空格）保留在结果中

#### Scenario: 缩进差异经行级匹配命中
- **WHEN** `old_text` 与文件内容的缩进风格不一致（如 2 空格对 4 空格），逐行去除首尾空白后连续行窗口仅命中一处
- **THEN** 命中的连续行区间整体替换为 `new_text`，替换后该区间缩进以 `new_text` 为准

### Requirement: 匹配唯一性约束

edit_file 工具 SHALL 在每一匹配层强制唯一性：当前层命中不少于 2 处时立即报错，报错信息 SHALL 包含命中处数与各处行号，并要求调用方扩大 `old_text` 上下文范围使其唯一。工具 SHALL 不做任何文件修改。

#### Scenario: 多处命中报错并列出行号
- **WHEN** 某一匹配层命中 3 处（如第 12、45、89 行）
- **THEN** 工具报错"old_text 在文件中匹配到 3 处（第 12、45、89 行），请扩大 old_text 范围加入上下文行使其唯一"，文件保持不变

### Requirement: 全部未命中的失败反馈

当四级匹配均未命中时，edit_file 工具 SHALL 报错并引导调用方重新确认文件内容。

#### Scenario: 未命中引导重新读取
- **WHEN** 四级匹配均未找到 `old_text`
- **THEN** 工具报错"未找到匹配。文件可能已被修改，请重新 read_file 后重试；注意 old_text 须与文件内容逐字一致"

### Requirement: 换行符输出语义

edit_file 工具 SHALL 将 `new_text` 中的换行符统一按 `\n` 写入，不做 CRLF 转换。第 2-4 层命中时整个文件 SHALL 以 LF 换行写回；第 1 层命中时仅替换区间变化，文件其余字节保持原样。

#### Scenario: new_text 统一按 LF 写入
- **WHEN** 替换在任一层执行
- **THEN** 写入的 `new_text` 换行符均为 `\n`

### Requirement: 成功反馈格式

替换成功时，edit_file 工具 SHALL 返回被替换的行号范围与命中的匹配层级。

#### Scenario: 成功返回行号与层级
- **WHEN** 精确匹配层在第 12-18 行唯一命中并完成替换
- **THEN** 返回信息包含文件相对路径、第 12-18 行与"精确匹配"层级标识

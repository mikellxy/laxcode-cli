## Purpose

在 agent 启动时发现、校验工作目录下的 skill 定义文件（`.laxcode/skills/<name>/SKILL.md`），并将其元信息以索引形式注入 system prompt，使大模型能够感知可用技能并按需读取技能正文，实现技能的渐进式披露。

## Requirements

### Requirement: Skill 发现范围

系统 SHALL 在 agent 启动时扫描 `<工作目录>/.laxcode/skills/` 下**恰好一层**子目录中的 `SKILL.md` 文件（文件名大小写敏感），不递归扫描更深层级，不读取 `skills/` 下的散置文件。

#### Scenario: 合法路径的 SKILL.md 被发现
- **WHEN** 存在 `.laxcode/skills/pdf-tools/SKILL.md`
- **THEN** 该文件被纳入 skill 解析流程

#### Scenario: skills 目录不存在时静默
- **WHEN** `<工作目录>/.laxcode/skills/` 目录不存在
- **THEN** skill 集合为空，不输出任何警告，不报错

#### Scenario: 嵌套目录不递归扫描
- **WHEN** 仅存在 `.laxcode/skills/foo/bar/SKILL.md`（SKILL.md 在二层目录内）
- **THEN** 该文件不被发现，skill 集合为空

#### Scenario: 散置文件与非精确文件名被忽略
- **WHEN** `skills/` 下存在 `README.md`、`skill.md`（大小写不符）或 `skills/pdf-tools/USAGE.md`
- **THEN** 这些文件均不被发现，不产生警告

### Requirement: Frontmatter 解析与必填字段

每个被发现的 SKILL.md SHALL 以 YAML frontmatter 开头（首行为 `---`，至下一个 `---` 行闭合），且 MUST 包含非空白的 `name` 与 `description` 字段。文件不满足该结构、或 frontmatter 不是合法 YAML 时，SHALL 跳过该 skill 并输出警告。

#### Scenario: 合法 frontmatter 通过
- **WHEN** SKILL.md 首行为 `---`，frontmatter 含 `name: pdf-tools` 与 `description: 根据文档内容生成 PDF`，随后以 `---` 闭合
- **THEN** 该 skill 通过解析，name 与 description 可用于索引

#### Scenario: description 含冒号正常解析
- **WHEN** frontmatter 中 `description: "deploy: how to deploy the service"`（含冒号的值以 YAML 引号包裹；不加引号时冒号加空格本身即 YAML 语法错误，走语法错误跳过路径）
- **THEN** description 被完整解析为 `deploy: how to deploy the service`

#### Scenario: 空文件跳过
- **WHEN** SKILL.md 为空文件
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: 无 frontmatter 跳过
- **WHEN** SKILL.md 是普通 Markdown，首行不是 `---`
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: frontmatter 未闭合跳过
- **WHEN** SKILL.md 首行为 `---` 但直到文件结束都没有第二个 `---` 行
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: YAML 语法错误跳过
- **WHEN** frontmatter 内容不是合法 YAML
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: 必填字段缺失或空白跳过
- **WHEN** frontmatter 缺少 `name` 或 `description`，或任一字段去除空白后为空
- **THEN** 该 skill 被跳过并输出警告

### Requirement: Skill 身份一致性

skill 的 `name` 字段 MUST 与其所在目录名完全一致，实现身份三方一致：目录名 == frontmatter name == 索引条目名。不一致时 SHALL 跳过该 skill 并输出警告，警告 SHALL 同时给出两种修复方向（修改 frontmatter 的 name 或重命名目录）。

#### Scenario: name 与目录名一致通过
- **WHEN** 目录为 `.laxcode/skills/pdf-tools/` 且 frontmatter `name: pdf-tools`
- **THEN** 该 skill 通过校验

#### Scenario: name 与目录名不一致跳过
- **WHEN** 目录为 `.laxcode/skills/pdf-tools/` 而 frontmatter `name: pdf`
- **THEN** 该 skill 被跳过，警告说明 name 与目录名不一致并提示两种修复方式

### Requirement: name 命名规范

skill 的 `name` MUST 匹配 `^[a-z0-9]+(-[a-z0-9]+)*$`（小写字母与数字，连字符仅作分隔，不允许首尾连字符或连续连字符）且长度不超过 64。不满足时 SHALL 跳过该 skill 并输出警告，警告 SHALL 包含命名规则说明。

#### Scenario: 合法 kebab-case 名称通过
- **WHEN** frontmatter `name: pdf-tools`（目录同名）
- **THEN** 该 skill 通过校验

#### Scenario: 大写字母被拒
- **WHEN** frontmatter `name: PdfTools`
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: 首尾或连续连字符被拒
- **WHEN** frontmatter `name: -pdf-tools-` 或 `name: pdf--tools`
- **THEN** 该 skill 被跳过并输出警告

#### Scenario: 名称超长被拒
- **WHEN** frontmatter `name` 长度超过 64 个字符（其余合规）
- **THEN** 该 skill 被跳过并输出警告

### Requirement: 索引段渲染

存在有效 skill 时，系统 SHALL 在 system prompt 中渲染 skill 索引段，包含：说明技能定义文件路径规则（`.laxcode/skills/<name>/SKILL.md`）与"技能不是工具、任务相关时先读取定义文件"的引导前言，以及每个 skill 一行 `- <name>: <description>` 的条目。条目 SHALL 按 name 升序排列，description 中的换行 SHALL 折叠为单个空格以保证条目单行。没有任何有效 skill 时，SHALL 整段省略，不输出空标题或占位文本。

#### Scenario: 索引段包含前言与条目
- **WHEN** 存在有效 skill `pdf-tools` 与 `commit`
- **THEN** system prompt 中的 skill 索引段包含路径规则说明、"技能不是工具"引导语，以及 `- commit: <其 description>` 与 `- pdf-tools: <其 description>` 两行条目

#### Scenario: 条目按名称升序且单行
- **WHEN** 某有效 skill 的 description 为多行 YAML（折叠语法），且存在多个 skill
- **THEN** 各条目按 name 升序排列，该 description 折叠后以单行呈现于条目中

#### Scenario: 零有效 skill 整段省略
- **WHEN** `.laxcode/skills/` 不存在、为空，或其中所有 SKILL.md 均未通过校验
- **THEN** system prompt 不包含 skill 索引段及任何相关占位文本

### Requirement: 索引加载时机与生命周期

skill 集合 SHALL 在 agent 会话启动时加载一次并注入该会话的 system prompt；会话进行期间对 `.laxcode/skills/` 的文件系统变更 SHALL NOT 反映到已注入的索引中。

#### Scenario: 启动时注入
- **WHEN** agent 启动且工作目录存在有效 skill
- **THEN** 首轮请求的 system prompt 已包含 skill 索引段

#### Scenario: 会话内变更不刷新
- **WHEN** 会话进行中新增或修改了一个 SKILL.md
- **THEN** 当前会话后续轮次的 system prompt 索引段保持启动时快照不变

### Requirement: 校验失败不阻断启动

任何 skill 的解析或校验失败 SHALL 仅导致该 skill 被跳过并输出警告，MUST NOT 阻断 agent 启动，MUST NOT 影响其他有效 skill 的加载。

#### Scenario: 有效与无效 skill 混存
- **WHEN** `.laxcode/skills/` 下同时存在一个合法 SKILL.md 与一个缺 description 的 SKILL.md
- **THEN** 合法 skill 正常进入索引，非法 skill 输出警告被跳过，agent 正常启动

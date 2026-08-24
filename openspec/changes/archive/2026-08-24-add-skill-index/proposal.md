## Why

LaxCode 目前无法扩展领域技能：system prompt 是静态模板，模型对"如何完成某类任务"的全部知识只能来自内置提示词。项目已经建立了 skill 的物理载体（`.laxcode/skills/<skill_name>/SKILL.md`，含 name/description 的 YAML frontmatter），但引擎从不读取它们——skill 体系有躯壳无灵魂。同时 `internal/context` 作为 README 规划的"Prompt 动态组装"包至今为空，本变更补上它的第一块基石。

## What Changes

- 新增 `internal/context` 包的 skill 索引能力：启动时扫描 `<workdir>/.laxcode/skills/*/SKILL.md`（仅一层、不递归），解析 YAML frontmatter
- 严格的 skill 合法性校验（任一不过即跳过并输出警告，绝不阻断启动）：
  - frontmatter 存在且可解析，`name` 与 `description` 均非空白
  - `name` 必须与所在目录名完全一致（身份三方一致：目录名 == frontmatter name == 索引条目名）
  - `name` 符合命名规范：`^[a-z0-9]+(-[a-z0-9]+)*$` 且长度 ≤ 64
- 在 `engine.Loop()` 构建 system prompt 时拼接 skill 索引段：路径规则说明 + `- name: description` 条目列表；零有效 skill 时整段省略
- 索引只含 frontmatter 元信息（渐进式披露）；skill 正文由模型按需通过现有 `read_file` 工具读取，不新增工具
- 填充 `.laxcode/skills/example/SKILL.md` 为合法样板（name 与目录一致、"何时使用"式 description 示范）
- `go.yaml.in/yaml/v4` 从 indirect 依赖提升为 direct

## Capabilities

### New Capabilities
- `context/skill-index`: skill 的发现、校验与 system prompt 索引注入——扫描 `.laxcode/skills/*/SKILL.md`、解析 frontmatter、执行合法性校验（含 name==dir_name 与命名规范）、渲染索引文本段及其零 skill 省略行为

### Modified Capabilities
<!-- 无：engine 侧仅在 Loop() 组装 system prompt 时消费新能力，属实现接线，无既有 spec 的需求变更 -->

## Impact

- **新增代码**：`internal/context/`（skill 加载、校验、索引渲染 + table-driven 测试及 `test_resource/` fixtures）
- **修改代码**：`internal/engine/sysprompt.go`（BuildSysPrompt 拼接索引段）、`internal/engine/main_loop.go`（Loop 中触发加载，预计仅数行）；engine 需为 `internal/context` 包加 import alias（与 stdlib `context` 冲突）
- **依赖**：`go.yaml.in/yaml/v4` 提升为 direct（已在依赖图中，无新增下载）
- **运行时行为**：启动时若存在非法 SKILL.md 会输出 `[LaxCode]` 前缀警告；`.laxcode/skills` 不存在时静默
- **不受影响**：provider、tools、Run 工具循环、消息 schema 均不动
- **已知局限（out-of-scope）**：skill 索引在启动时加载一次，会话中途新增/修改 SKILL.md 不会刷新索引；索引刷新机制（显式指令或 fsnotify）留待后续变更

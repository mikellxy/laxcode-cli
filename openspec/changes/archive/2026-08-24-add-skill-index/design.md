## Context

见 proposal.md（Why / What Changes）。现状约束：`internal/context` 为空包；system prompt 由 `../../../../internal/context/sysprompt.go` 的 `BuildSysPrompt(workDir)` 静态生成，`Loop()` 开头一次性注入；`go.yaml.in/yaml/v4` 已作为 indirect 依赖存在于 go.sum（anthropic SDK 传递引入）；skill 正文读取可完全复用现有 `read_file` 工具（`.laxcode` 在工作目录内，`safeJoinWorkDir` 天然放行）。

## Goals / Non-Goals

**Goals:**

- `internal/context` 包提供独立的 skill 发现、校验、索引渲染能力，engine 只做接线
- 校验失败一律本地化为警告，启动路径上零 error 传播
- 索引输出确定性（固定排序、固定文案模板），便于测试断言与上游 prompt 前缀稳定（利于 provider 端 prompt caching）

**Non-Goals:**

- 不新增任何工具（skill 正文经现有 `read_file` 读取）
- 不做会话内索引刷新（fsnotify / 显式刷新指令）
- 不解析、不校验 skill 正文（frontmatter 之后的内容原样留给模型按需读）
- 不引入 skill 启用/禁用、版本、来源等元数据扩展

## Decisions

### D1: 包结构与 API —— 两个函数，不建框架

`internal/context` 新增 `skill.go`：

```go
type Skill struct {
    Name        string // == 目录名 == frontmatter name
    Description string // 渲染前保留原样（可含换行）
}

func LoadSkills(workDir string) []Skill      // 扫描 + 解析 + 校验，内部消化所有错误为警告
func RenderSkillIndex(skills []Skill) string // 纯函数，零 skill 返回 ""
```

- `LoadSkills` 显式接收 `workDir` 参数而非读 `env.WorkDir` 全局——与 `NewAgentEngine` 传参风格一致，测试可用临时目录隔离
- 渲染拆成纯函数 `RenderSkillIndex`，与 IO 彻底解耦，表驱动测试无文件系统依赖
- v1 不抽象 `Assembler`/`PromptSection` 之类的框架。README 中 context 包的 charter（"Prompt 动态组装"）会有第二、第三个动态段（memory、项目约定），届时再抽公共接口（YAGNI，但命名上已留生长空间）

**被拒方案**：单个 `BuildSystemPrompt(workDir)` 大函数包办一切——IO 与渲染耦合，且 engine 侧已有的静态模板会被迫搬进 context 包，改动面变大。

### D2: frontmatter 解析 —— go.yaml.in/yaml/v4（indirect 提升 direct）

- 库已在依赖图中，提升为零新增下载成本
- 白拿完整 YAML 语义：description 含冒号、带引号、多行折叠（`>-`）等场景全部正确处理
- 解析目标为最小 struct：`struct { Name string; Description string }`，未知字段忽略（宽容，向前兼容）

**被拒方案**：手写 `strings.Cut(line, ":")` 极简解析——单行 description 勉强够用，但多行/引号/含 `#` 注释等写法会静默解析错，且省下的只是一行 import。

### D3: frontmatter 边界识别 —— 手工切分 + 库解析

1. 按行读取，首行去除 BOM 后 trim 空格必须等于 `---`（否则判"无 frontmatter"）
2. 向后找下一个 trim 后等于 `---` 的行作为闭合；到 EOF 未找到判"未闭合"
3. 两边界之间的行拼接后交给 `yaml.Unmarshal`

边界识别不交给 YAML 库（`---` 是文档分隔符语义，混在一起解析容易把正文误吞），只把边界内的键值解析交给库。

### D4: 校验管线 —— 顺序固定，每步独立警告

```
① frontmatter 结构（首行 --- / 闭合 ---）   ② YAML 解析
③ name、description 非空白                  ④ name == 目录名
⑤ name 匹配 ^[a-z0-9]+(-[a-z0-9]+)*$ 且 ≤64
```

- 顺序即警告优先级：先报结构性问题再报内容问题，skill 作者修一个警告就能推进一步
- ④ 与 ⑤ 的警告不合并：④ 提示"改 frontmatter 或改目录名"两种修法，⑤ 附命名规则原文——警告本身可直接照做
- ⑤ 同时隐式过滤隐藏目录（`.foo` 首字符即不合法），无需单独分支

### D5: name == dir_name 消灭了重名处理

探索期曾计划"重名 → warn + 后者覆盖"。D6（身份一致性强制）定案后，同一 `skills/` 父目录下目录名唯一 ⇒ name 全局唯一 ⇒ **重名在结构上不可能发生**。因此实现无冲突分支，`LoadSkills` 直接用 `[]Skill` + `sort`（按 Name 升序），不需要 map 去重。这是 D6 超出 token 节省之外的第三重收益。

### D6: 排序与确定性输出

条目按 name 字节序升序（`sort.Slice`）。动机：同一磁盘状态 ⇒ 逐字节相同的 system prompt，测试可全量断言；system prompt 是每轮请求的前缀，稳定前缀对 provider 端 prompt caching 友好。

### D7: 索引段形态与注入点

渲染模板（`RenderSkillIndex` 内置）：

```
## 可用技能（Skills）

以下是可用技能索引。技能不是工具，无法直接调用；当任务与某技能相关时，
先读取其定义文件 .laxcode/skills/<技能名>/SKILL.md，再按文件内容指引完成任务。
与任务无关的技能请忽略。

- <name>: <description>
```

- 前言只承载两个语义点：路径规则（模型可自行拼出 path，条目里不重复路径）、防把 skill 当 tool 调用
- description 渲染前把连续空白折叠为单个空格（含换行），保证条目单行
- 注入点：`engine.Loop()` 中 `BuildSysPrompt` 之前先 `LoadSkills`；`BuildSysPrompt` 签名改为接收索引文本（或 skills 切片）——倾向 `BuildSysPrompt(workDir string, skills []laxctx.Skill)`，让模板拼接职责留在 engine（静态模板归 engine，动态数据归 context）
- engine 侧 import alias：`laxctx "github.com/mikellxy/laxcode/internal/context"`（与 stdlib `context.Context` 冲突）
- `engine_test.go` 中 `BuildSysPrompt(e.WorkingDir)` 调用点同步适配

### D8: 警告输出惯例

沿用项目现有控制台风格：`fmt.Printf("\033[33m[LaxCode] ...\033[0m\n", ...)`（黄色，同 tool 执行提示）。警告发生在 `LoadSkills` 内部（加载即报，逐条打印），不聚合。

### D9: example/SKILL.md 样板

填为合法样板，兼作手工冒烟样本：`name: example`（与目录一致）、description 示范"何时使用"写法（如 `Use when 用户询问示例技能的用法...`），正文给一小段说明性内容。

## Risks / Trade-offs

- [skill description 注入 system prompt] description 是任意文本，理论可携带影响模型行为的指令 → 缓解：`.laxcode/` 与项目代码同信任边界，能写 SKILL.md 就能写源码，不引入新的攻击面；条目强制单行折叠也限制了结构化注入的形态
- [模型仍可能幻觉 skill 工具调用] → 缓解：前言显式声明"技能不是工具"；观察期为已知的模型行为问题，不靠代码兜底
- [description 很长导致索引膨胀] 索引在每轮请求重复携带 → 缓解：v1 不截断（description 语义完整优先），真实膨胀出现后再加长度上限；该风险由 skill 作者侧约定控制
- [会话内 skill 变更不生效] 用户可能视为 bug → 已在 proposal 记为 out-of-scope，警告不负责提示（避免噪音）
- [macOS 大小写不敏感文件系统] 目录 `Foo`/`foo` 在 APFS 默认配置下视为同一目录，与命名规范校验（大小写敏感）存在理论错位 → 影响极小：`Foo` 目录本身过不了 ⑤，不构成可利用的歧义

## Migration Plan

新能力叠加，无数据迁移。步骤：go.mod 提升 direct 依赖 → 新增 context 包（含测试）→ engine 接线 → 填充 example/SKILL.md → `go build ./...` + `go test ./...` 全绿即完成。回滚 = revert 整个 change，无残留状态。

## Open Questions

- 索引前言文案的最终措辞（D7 给出基准稿）——实现时可微调用词，不改变结构语义与两个必须承载的信息点

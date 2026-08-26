## 1. 依赖与包基础

- [x] 1.1 将 `go.yaml.in/yaml/v4` 提升为 direct 依赖（`go get` 后 `go mod tidy`，确认 go.mod require 主块出现且构建通过）
- [x] 1.2 创建 `internal/context/skill.go`：定义 `Skill` struct（Name/Description）与 `LoadSkills(workDir string) []Skill`、`RenderSkillIndex(skills []Skill) string` 的函数骨架与包注释

## 2. Skill 加载与校验（internal/context）

- [x] 2.1 实现扫描：`os.ReadDir` 遍历 `<workdir>/.laxcode/skills/` 恰好一层子目录，仅匹配文件名严格为 `SKILL.md`（大小写敏感）的条目；目录不存在时静默返回空切片；结果按 Name 字节序升序
- [x] 2.2 实现 frontmatter 边界识别：首行去 BOM 后 trim 空格须为 `---`，向后找下一个 trim 后为 `---` 的行闭合；无首行分隔符/未闭合/空文件 → 跳过并警告
- [x] 2.3 实现 frontmatter YAML 解析：边界内内容 `yaml.Unmarshal` 到最小 struct（Name/Description，未知字段忽略）；解析失败 → 跳过并警告；`name` 或 `description` 去除空白后为空 → 跳过并警告
- [x] 2.4 实现身份与命名校验：`name != 目录名` → 跳过并警告（文案含"改 frontmatter name 或重命名目录"两种修法）；`name` 不匹配 `^[a-z0-9]+(-[a-z0-9]+)*$` 或长度 > 64 → 跳过并警告（文案附命名规则说明）
- [x] 2.5 所有警告采用现有控制台惯例（黄色 `[LaxCode]` 前缀，`fmt.Printf`），任何失败路径不得返回 error 或 panic

## 3. 索引渲染与 engine 接线

- [x] 3.1 实现 `RenderSkillIndex`：按 design D7 模板渲染（标题 + 路径规则/"技能不是工具"前言 + `- <name>: <description>` 条目）；description 渲染前将连续空白（含换行）折叠为单个空格；入参为空切片时返回空字符串（零 skill 整段省略）
- [x] 3.2 修改 `../../../../internal/context/sysprompt.go`：`BuildSysPrompt` 签名改为接收 `[]Skill`（import alias `laxctx`），在静态模板后追加索引段
- [x] 3.3 修改 `../../../../internal/engine/engine.go`：`Loop()` 开头调用 `laxctx.LoadSkills(f.WorkingDir)` 并传给 `BuildSysPrompt`；同步适配 `engine_test.go` 中的既有调用点

## 4. 样板与测试验证

- [x] 4.1 填充 `.laxcode/skills/example/SKILL.md`：`name: example`（与目录一致）、"何时使用"式 description、一小段说明正文
- [x] 4.2 编写 `internal/context/skill_test.go`：表驱动覆盖 spec 全部场景——发现范围（目录不存在/嵌套不递归/散置文件/大小写）、frontmatter（合法/含冒号/空文件/无分隔符/未闭合/YAML 错误/字段空白）、身份一致性（一致/不一致）、命名规范（合法/大写/连字符/超长）、渲染（前言与条目/排序单行/零省略）、混存容错（有效+无效共存）；skills 树用 `t.TempDir()` 动态构造或 `test_resource/` fixtures（参照 `internal/utils` 惯例）
- [x] 4.3 全量验证：`go build ./...` 与 `go test ./...` 全绿；在仓库根手工冒烟启动 REPL，确认 system prompt 含 example 技能索引且无警告误报
- [x] 4.4 （用户批准的 scope 扩展）修复存量 `internal/provider/provider_test.go` 编译错误：适配 `Generate` 返回 `[]schema.Message` 的新签名，使 `go test ./...` 全绿

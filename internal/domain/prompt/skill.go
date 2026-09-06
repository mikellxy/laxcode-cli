package prompt

// 技能（skill）索引能力：在 agent 启动时发现并校验工作目录下的
// .laxcode/skills/<name>/SKILL.md 定义文件，把 frontmatter 元信息渲染为
// system prompt 中的技能索引段，供大模型按需读取技能正文（渐进式披露）。
//
// 职责边界：本包只做「解析 + 校验 + 渲染」三件纯函数的事。文件发现交给
// SkillSource 端口（实现见 infrastructure/skillrepo），警告的落点交给调用方
// 注入的 warn 回调——领域层既不碰磁盘，也不决定往哪个流写。
// 任何单个技能校验失败只跳过该技能，绝不阻断 agent 启动。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Skill 是通过全部校验的技能元信息：Name 与所在目录名、frontmatter name 三方一致。
type Skill struct {
	Name        string
	Description string
}

// frontmatterYAML 是 SKILL.md frontmatter 的最小解析目标，未知字段忽略（向前兼容）。
type frontmatterYAML struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// skillNamePattern 约束技能名：小写字母与数字，连字符仅作分隔（不允许首尾
// 或连续连字符），同时隐式排除 . 开头的隐藏目录。
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// skillNameMaxLen 是技能名长度上限。
const skillNameMaxLen = 64

// SkillFile 是一份「已被发现、尚未解析」的技能定义文件：DirName 是其所在
// 目录名（技能身份的一部分，须与 frontmatter 的 name 完全一致），Content 是
// SKILL.md 全文。
type SkillFile struct {
	DirName string
	Content string
}

// SkillSource 是技能定义文件的发现端口：领域层只定义契约，真正的目录扫描由
// 基础设施实现（internal/infrastructure/skillrepo）。
//
// workDir 作为方法参数而非构造期绑定，使同一个无状态实例可同时服务主 Agent
// 与使用不同工作目录的子 Agent。
type SkillSource interface {
	// List 返回 workDir 下发现的技能定义文件，顺序不做保证；没有技能时返回空。
	// 发现层面的缺失（skills 目录不存在、散置文件、嵌套更深的 SKILL.md、文件名
	// 大小写不符）一律静默，MUST NOT 报错也 MUST NOT 产生警告——只有解析与校验
	// 失败才需要警告，而那属领域层职责。见 openspec/specs/context/skill-index。
	List(workDir string) []SkillFile
}

// LoadSkills 解析并校验 src 在 workDir 下发现的技能定义文件，返回有效技能集合
// （按 Name 升序；无有效技能时返回 nil，使调用方可直接与 nil 比较）。
//
// src 为 nil 时返回 nil，便于未装配技能源的调用方（如测试）直接传 nil。
// warn 非 nil 时，每个被跳过的技能交出一条已含目录名、失败原因与修复方向的
// 警告文案；warn 为 nil 则完全静默。无论哪种情况都不阻断其余技能的加载。
func LoadSkills(src SkillSource, workDir string, warn func(string)) []Skill {
	if src == nil {
		return nil
	}

	var skills []Skill
	for _, file := range src.List(workDir) {
		skill, skipReason := parseSkill(file.Content, file.DirName)
		if skipReason != "" {
			if warn != nil {
				warn(skipReason)
			}
			continue
		}
		skills = append(skills, skill)
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// parseSkill 执行校验管线，顺序即优先级：
// ① frontmatter 边界 ② YAML 解析 ③ 必填字段非空白
// ④ name 与目录名一致 ⑤ name 符合命名规范。
// 任一步失败返回空 Skill 与面向用户的跳过原因（含修复方向）；全部通过时
// 原因为空串。本函数是纯函数，不触磁盘、不写任何流。
func parseSkill(content, dirName string) (Skill, string) {
	fmText, reason := extractFrontmatter(content)
	if reason != "" {
		return Skill{}, skipMsg(dirName, reason)
	}

	var fm frontmatterYAML
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Skill{}, skipMsg(dirName, "frontmatter 不是合法 YAML: "+err.Error())
	}

	if strings.TrimSpace(fm.Name) == "" || strings.TrimSpace(fm.Description) == "" {
		return Skill{}, skipMsg(dirName, "frontmatter 缺少非空白的 name 或 description 字段")
	}

	// 身份三方一致：目录名 == frontmatter name == 索引条目名。规范要求警告
	// 同时给出两种修复方向，故两个候选值都要写进文案。
	if fm.Name != dirName {
		return Skill{}, skipMsg(dirName, fmt.Sprintf(
			"frontmatter 的 name %q 与所在目录名 %q 不一致；请把 name 改为 %q，或把目录重命名为 %q",
			fm.Name, dirName, dirName, fm.Name))
	}

	// 长度与字符集分开判定，跳过行为一致，但警告可指出究竟违反了哪一条。
	if len(fm.Name) > skillNameMaxLen {
		return Skill{}, skipMsg(dirName, fmt.Sprintf(
			"name 长度 %d 超过上限 %d（命名规则：%s，且长度不超过 %d）",
			len(fm.Name), skillNameMaxLen, skillNameRule, skillNameMaxLen))
	}
	if !skillNamePattern.MatchString(fm.Name) {
		return Skill{}, skipMsg(dirName, fmt.Sprintf(
			"name %q 不符合命名规则（%s）", fm.Name, skillNameRule))
	}

	return Skill{Name: fm.Name, Description: fm.Description}, ""
}

// skillNameRule 是命名规则的人类可读说明，用于跳过警告。它须与
// skillNamePattern / skillNameMaxLen 保持一致。
const skillNameRule = "小写字母与数字，连字符仅作分隔，不允许首尾或连续连字符，长度不超过 64"

// skipMsg 拼装技能跳过警告：统一带上技能目录名，便于用户定位到具体文件。
func skipMsg(dirName, reason string) string {
	return fmt.Sprintf("技能 %s 已被跳过：%s", dirName, reason)
}

// extractFrontmatter 从 SKILL.md 内容中切出 frontmatter 文本（不含首尾分隔行）。
// 首行（去 BOM 与空白后）必须为 ---，其后第一个 trim 后为 --- 的行闭合；
// 不满足时返回失败原因（空串表示成功），该原因会经 parseSkill 汇入跳过警告。
func extractFrontmatter(content string) (fmText, failReason string) {
	if content == "" {
		return "", "文件为空，缺少 YAML frontmatter"
	}

	lines := strings.Split(content, "\n")
	first := strings.TrimPrefix(lines[0], "\ufeff")
	if strings.TrimSpace(first) != "---" {
		return "", "缺少 YAML frontmatter（首行须为 ---）"
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), ""
		}
	}
	return "", "frontmatter 未闭合（缺少第二个 --- 行）"
}

// RenderSkillIndex 将技能集合渲染为 system prompt 中的技能索引段：
// 前言说明技能定义文件路径规则并声明“技能不是工具”，其后每行一条
// "- <name>: <description>" 条目（description 折叠为单行）。
// 零技能时返回空字符串，整段省略。
//
// 前言里的 .laxcode/skills/<技能名>/SKILL.md 是向模型描述的相对路径，属模型
// 可见文案而非代码取路径，故必须与 infrastructure/layout 的实际布局同步修改。
func RenderSkillIndex(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 可用技能（Skills）\n\n")
	b.WriteString("以下是可用技能索引。技能不是工具，无法直接调用；当任务与某技能相关时，\n")
	b.WriteString("先读取其定义文件 .laxcode/skills/<技能名>/SKILL.md，再按文件内容指引完成任务。\n")
	b.WriteString("与任务无关的技能请忽略。\n\n")
	for _, skill := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", skill.Name, collapseWhitespace(skill.Description))
	}
	return b.String()
}

// collapseWhitespace 将连续空白（含换行、制表符）折叠为单个空格并去除首尾空白。
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

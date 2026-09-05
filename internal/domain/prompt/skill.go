package prompt

// 技能（skill）索引能力：在 agent 启动时发现并校验工作目录下的
// .laxcode/skills/<name>/SKILL.md 定义文件，把 frontmatter 元信息渲染为
// system prompt 中的技能索引段，供大模型按需读取技能正文（渐进式披露）。
// 校验失败的目录一律静默跳过、绝不阻断启动；警告输出暂缺，后续再定落点。

import (
	"fmt"
	"os"
	"path/filepath"
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

// LoadSkills 扫描 <workDir>/.laxcode/skills/ 下恰好一层子目录中的 SKILL.md，
// 解析并校验后返回有效技能集合（按 Name 升序）。
// skills 目录不存在时静默返回空集合；任何非法文件静默跳过，绝不阻断启动。
func LoadSkills(workDir string) []Skill {
	skillsRoot := filepath.Join(workDir, ".laxcode", "skills")
	dirEntries, err := os.ReadDir(skillsRoot)
	if err != nil {
		// skills 目录不存在（或不可读）视为没有技能
		return nil
	}

	var skills []Skill
	for _, dirEntry := range dirEntries {
		if !dirEntry.IsDir() {
			continue // skills/ 下的散置文件直接忽略
		}
		dirName := dirEntry.Name()
		if skill, ok := loadSkillDir(filepath.Join(skillsRoot, dirName), dirName); ok {
			skills = append(skills, skill)
		}
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// loadSkillDir 在单个技能目录内定位严格命名为 SKILL.md（大小写敏感）的文件，
// 读取并完成全部校验；目录内没有 SKILL.md 时静默忽略。
func loadSkillDir(dirPath, dirName string) (Skill, bool) {
	// 用目录列表精确匹配文件名，避免大小写不敏感文件系统上 Stat 误命中 skill.md
	fileEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return Skill{}, false
	}

	found := false
	for _, fileEntry := range fileEntries {
		if !fileEntry.IsDir() && fileEntry.Name() == "SKILL.md" {
			found = true
			break
		}
	}
	if !found {
		return Skill{}, false
	}

	content, err := os.ReadFile(filepath.Join(dirPath, "SKILL.md"))
	if err != nil {
		return Skill{}, false
	}
	return parseSkill(string(content), dirName)
}

// parseSkill 执行校验管线，顺序即优先级：
// ① frontmatter 边界 ② YAML 解析 ③ 必填字段非空白
// ④ name 与目录名一致 ⑤ name 符合命名规范；任一步失败即静默跳过。
func parseSkill(content, dirName string) (Skill, bool) {
	fmText, failReason := extractFrontmatter(content)
	if failReason != "" {
		return Skill{}, false
	}

	var fm frontmatterYAML
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Skill{}, false
	}

	if strings.TrimSpace(fm.Name) == "" || strings.TrimSpace(fm.Description) == "" {
		return Skill{}, false
	}

	if fm.Name != dirName {
		return Skill{}, false
	}

	if len(fm.Name) > skillNameMaxLen || !skillNamePattern.MatchString(fm.Name) {
		return Skill{}, false
	}

	return Skill{Name: fm.Name, Description: fm.Description}, true
}

// extractFrontmatter 从 SKILL.md 内容中切出 frontmatter 文本（不含首尾分隔行）。
// 首行（去 BOM 与空白后）必须为 ---，其后第一个 trim 后为 --- 的行闭合；
// 不满足时返回失败原因（空串表示成功）。failReason 目前仅用于成功/失败判定，
// 文案保留以便后续补回警告输出。
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

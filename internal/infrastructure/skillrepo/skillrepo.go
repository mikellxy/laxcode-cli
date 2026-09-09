// Package skillrepo 是 prompt.SkillSource 端口的文件系统实现：扫描工作目录下
// ${workDir}/.laxcode/skills/ 恰好一层子目录中的 SKILL.md，把原文交给领域层
// 解析校验。路径布局取自 infrastructure/layout，本包只负责「发现」，
// 不做任何 frontmatter 解析或字段校验——那是 domain/prompt 的职责。
package skillrepo

import (
	"os"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// skillFileName 是技能定义文件的精确名称（大小写敏感）。
const skillFileName = "SKILL.md"

// Source 是无状态的技能发现实现：workDir 由每次 List 调用传入，故同一个
// 实例可被主 Agent 与使用不同工作目录的子 Agent 共享。
type Source struct{}

// New 返回技能发现端口的文件系统实现。
func New() *Source { return &Source{} }

var _ prompt.SkillSource = (*Source)(nil)

// List 返回 ${workDir}/.laxcode/skills/ 下恰好一层子目录中的 SKILL.md 原文，
// 每条带上其所在目录名（技能身份校验需要它）。
//
// 以下情形一律静默忽略，既不报错也不产生警告（规范要求发现层面的缺失对
// 用户不可见）：skills 目录不存在或不可读、skills 根下的散置文件、嵌套更深
// 层级里的 SKILL.md（不递归）、技能目录内文件名大小写不符、读取失败。
func (Source) List(workDir string) []prompt.SkillFile {
	skillsRoot := layout.SkillsRoot(workDir)
	dirEntries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return nil // skills 目录不存在（或不可读）视为没有技能
	}

	var files []prompt.SkillFile
	for _, dirEntry := range dirEntries {
		if !dirEntry.IsDir() {
			continue // skills/ 下的散置文件直接忽略
		}
		dirName := dirEntry.Name()
		if content, ok := readSkillFile(filepath.Join(skillsRoot, dirName)); ok {
			files = append(files, prompt.SkillFile{DirName: dirName, Content: content})
		}
	}
	return files
}

// readSkillFile 在单个技能目录内定位精确命名的 SKILL.md 并读取全文；目录不可读、
// 没有该文件或读取失败时返回 ok=false。
func readSkillFile(dirPath string) (string, bool) {
	// 用目录列表精确比对文件名，而非 os.Stat：在大小写不敏感的文件系统
	// （macOS / Windows 默认）上 Stat("SKILL.md") 会误命中 skill.md，
	// 而规范要求文件名大小写敏感。
	fileEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", false
	}

	found := false
	for _, fileEntry := range fileEntries {
		if !fileEntry.IsDir() && fileEntry.Name() == skillFileName {
			found = true
			break
		}
	}
	if !found {
		return "", false
	}

	content, err := os.ReadFile(filepath.Join(dirPath, skillFileName))
	if err != nil {
		return "", false
	}
	return string(content), true
}

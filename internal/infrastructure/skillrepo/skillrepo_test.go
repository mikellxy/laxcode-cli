package skillrepo

// 技能「发现」层面的行为测试：全部依赖真实文件系统（目录是否存在、是否递归、
// 文件名大小写、散置文件），故留在基础设施侧。frontmatter 解析与字段校验的
// 用例见 domain/prompt/skill_test.go —— 那边用内存替身，完全不触磁盘。

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// writeSkillFile 在 ${root}/.laxcode/skills/<dirName>/ 下写入 fileName，父目录
// 自动创建。fileName 参数化以便覆盖「大小写不符」与「同目录其它文件」的用例。
func writeSkillFile(t *testing.T, root, dirName, fileName, content string) {
	t.Helper()
	dir := filepath.Join(layout.SkillsRoot(root), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", fileName, err)
	}
}

// listSorted 取 List 结果并按 DirName 排序，抹平目录遍历顺序的平台差异
// （端口契约本身不保证顺序）。
func listSorted(t *testing.T, workDir string) []prompt.SkillFile {
	t.Helper()
	got := New().List(workDir)
	sort.Slice(got, func(i, j int) bool { return got[i].DirName < got[j].DirName })
	return got
}

// dirNames 抽出发现结果的目录名列表，便于只关心「发现了哪些技能目录」的断言。
func dirNames(files []prompt.SkillFile) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.DirName)
	}
	return names
}

func TestListMissingSkillsDir(t *testing.T) {
	// skills 目录不存在视为没有技能：返回空且不报错（规范要求静默）
	if got := New().List(t.TempDir()); len(got) != 0 {
		t.Errorf("skills 目录不存在时应返回空，实际 %+v", got)
	}
}

func TestListReturnsDirNameAndFullContent(t *testing.T) {
	root := t.TempDir()
	content := "---\nname: pdf-tools\ndescription: 生成 PDF\n---\n\n# 正文\n多行内容\n"
	writeSkillFile(t, root, "pdf-tools", "SKILL.md", content)
	// 同目录下的其它文件不影响发现，也不被误当作技能正文
	writeSkillFile(t, root, "pdf-tools", "USAGE.md", "无关文件")

	want := []prompt.SkillFile{{DirName: "pdf-tools", Content: content}}
	if got := listSorted(t, root); !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v, want %+v", got, want)
	}
}

func TestListFindsAllSkillDirs(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "commit", "SKILL.md", "c")
	writeSkillFile(t, root, "pdf-tools", "SKILL.md", "p")
	// 目录存在但没有 SKILL.md：静默忽略
	writeSkillFile(t, root, "no-def", "README.md", "没有 SKILL.md")

	got := listSorted(t, root)
	if want := []string{"commit", "pdf-tools"}; !reflect.DeepEqual(dirNames(got), want) {
		t.Errorf("发现的技能目录 = %v, want %v", dirNames(got), want)
	}
}

func TestListDoesNotRecurse(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(layout.SkillsRoot(root), "foo", "bar")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建嵌套目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写入嵌套 SKILL.md 失败: %v", err)
	}

	// 只扫恰好一层：foo/bar/SKILL.md 不被发现，foo 自身也没有 SKILL.md
	if got := New().List(root); len(got) != 0 {
		t.Errorf("二层目录内的 SKILL.md 不应被发现，实际 %+v", got)
	}
}

func TestListIgnoresLooseFiles(t *testing.T) {
	root := t.TempDir()
	skillsRoot := layout.SkillsRoot(root)
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("创建 skills 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "README.md"), []byte("散置文件"), 0o644); err != nil {
		t.Fatalf("写入散置文件失败: %v", err)
	}

	if got := New().List(root); len(got) != 0 {
		t.Errorf("skills 根下的散置文件应被忽略，实际 %+v", got)
	}
}

// TestListIgnoresFileNameCaseMismatch 守住「文件名大小写敏感」：在大小写不敏感的
// 文件系统（macOS / Windows 默认）上，skill.md 不得被当成 SKILL.md 命中。
// 实现用目录列表精确比对而非 os.Stat，正是为了这一点。
func TestListIgnoresFileNameCaseMismatch(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "foo", "skill.md", "---\nname: foo\ndescription: d\n---\n")

	if got := New().List(root); len(got) != 0 {
		t.Errorf("skill.md（大小写不符）应被忽略，实际 %+v", got)
	}
}

// TestListDoesNotValidate 守住职责边界：发现层不做任何 frontmatter 解析或字段
// 校验，非法内容同样原样交给领域层，由后者决定跳过与警告文案。
func TestListDoesNotValidate(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "bad", "SKILL.md", "# 没有 frontmatter\n")

	want := []prompt.SkillFile{{DirName: "bad", Content: "# 没有 frontmatter\n"}}
	if got := listSorted(t, root); !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v, want %+v", got, want)
	}
}

// TestSourceIsStatelessAcrossWorkDirs 验证 workDir 按调用传入而非构造期绑定：
// 同一个实例要能同时服务主 Agent 与跑在不同工作目录下的子 Agent。
func TestSourceIsStatelessAcrossWorkDirs(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeSkillFile(t, first, "a", "SKILL.md", "A")
	writeSkillFile(t, second, "b", "SKILL.md", "B")

	src := New()
	if got := src.List(first); len(got) != 1 || got[0].DirName != "a" {
		t.Errorf("第一个工作目录: List() = %+v, want 只发现 a", got)
	}
	if got := src.List(second); len(got) != 1 || got[0].DirName != "b" {
		t.Errorf("第二个工作目录: List() = %+v, want 只发现 b", got)
	}
	// 回头再查第一个工作目录，结果不受上一次调用影响
	if got := src.List(first); len(got) != 1 || got[0].DirName != "a" {
		t.Errorf("重查第一个工作目录: List() = %+v, want 只发现 a", got)
	}
}

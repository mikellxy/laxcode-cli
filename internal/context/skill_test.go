package context

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/printer"
)

// writeSkillFile 在 root/.laxcode/skills/<dirName>/ 下写入 SKILL.md，父目录自动创建。
func writeSkillFile(t *testing.T, root, dirName, content string) {
	t.Helper()
	dir := filepath.Join(root, ".laxcode", "skills", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md 失败: %v", err)
	}
}

// validSkillMD 生成以 name/description 为 frontmatter 的合法 SKILL.md 内容。
func validSkillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n正文：按指引完成任务。\n", name, description, name)
}

func TestLoadSkills(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  []Skill
	}{
		{
			name:  "skills 目录不存在返回空集合",
			setup: func(t *testing.T, root string) {},
			want:  nil,
		},
		{
			name: "合法 frontmatter 通过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "pdf-tools", validSkillMD("pdf-tools", "根据文档内容生成 PDF"))
			},
			want: []Skill{{Name: "pdf-tools", Description: "根据文档内容生成 PDF"}},
		},
		{
			name: "description 含冒号时以引号包裹完整解析",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "deploy", "---\nname: deploy\ndescription: \"deploy: how to deploy the service\"\n---\n")
			},
			want: []Skill{{Name: "deploy", Description: "deploy: how to deploy the service"}},
		},
		{
			name: "description 冒号后无空格时无需引号",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "deploy2", "---\nname: deploy2\ndescription: deploy:how to deploy\n---\n")
			},
			want: []Skill{{Name: "deploy2", Description: "deploy:how to deploy"}},
		},
		{
			name: "多行 description 经 YAML 折叠语法解析",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "long", "---\nname: long\ndescription: >-\n  第一行\n  第二行\n---\n")
			},
			want: []Skill{{Name: "long", Description: "第一行 第二行"}},
		},
		{
			name: "多个技能按 name 升序返回",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "pdf-tools", validSkillMD("pdf-tools", "根据文档内容生成 PDF"))
				writeSkillFile(t, root, "commit", validSkillMD("commit", "生成规范 commit message"))
				writeSkillFile(t, root, "bash-helper", validSkillMD("bash-helper", "bash 用法助手"))
			},
			want: []Skill{
				{Name: "bash-helper", Description: "bash 用法助手"},
				{Name: "commit", Description: "生成规范 commit message"},
				{Name: "pdf-tools", Description: "根据文档内容生成 PDF"},
			},
		},
		{
			name: "空文件跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "empty", "")
			},
			want: nil,
		},
		{
			name: "无 frontmatter 跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "plain", "# 普通 Markdown\n没有 frontmatter\n")
			},
			want: nil,
		},
		{
			name: "frontmatter 未闭合跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "unclosed", "---\nname: unclosed\ndescription: x\n")
			},
			want: nil,
		},
		{
			name: "YAML 语法错误跳过（未闭合流式序列）",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "bad-yaml", "---\nname: bad-yaml\ndescription: x\nextra: [unclosed\n---\n")
			},
			want: nil,
		},
		{
			name: "未加引号的冒号空格构成 YAML 语法错误跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "bad-colon", "---\nname: bad-colon\ndescription: deploy: how to deploy\n---\n")
			},
			want: nil,
		},
		{
			name: "name 空白跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "blank-name", "---\nname: \"  \"\ndescription: x\n---\n")
			},
			want: nil,
		},
		{
			name: "description 缺失跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "no-desc", "---\nname: no-desc\n---\n")
			},
			want: nil,
		},
		{
			name: "name 与目录名不一致跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "pdf-tools", validSkillMD("pdf", "描述"))
			},
			want: nil,
		},
		{
			name: "大写字母名称跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "PdfTools", validSkillMD("PdfTools", "描述"))
			},
			want: nil,
		},
		{
			name: "首尾连字符名称跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "-pdf-", validSkillMD("-pdf-", "描述"))
			},
			want: nil,
		},
		{
			name: "连续连字符名称跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "pdf--tools", validSkillMD("pdf--tools", "描述"))
			},
			want: nil,
		},
		{
			name: "隐藏目录名称跳过",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, ".hidden", validSkillMD(".hidden", "描述"))
			},
			want: nil,
		},
		{
			name: "名称超过 64 字符跳过",
			setup: func(t *testing.T, root string) {
				long := strings.Repeat("a", 65)
				writeSkillFile(t, root, long, validSkillMD(long, "描述"))
			},
			want: nil,
		},
		{
			name: "嵌套目录不递归扫描",
			setup: func(t *testing.T, root string) {
				nested := filepath.Join(root, ".laxcode", "skills", "foo", "bar")
				if err := os.MkdirAll(nested, 0o755); err != nil {
					t.Fatalf("创建嵌套目录失败: %v", err)
				}
				if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte(validSkillMD("bar", "描述")), 0o644); err != nil {
					t.Fatalf("写入嵌套 SKILL.md 失败: %v", err)
				}
			},
			want: nil,
		},
		{
			name: "skills 根下的散置文件被忽略",
			setup: func(t *testing.T, root string) {
				skills := filepath.Join(root, ".laxcode", "skills")
				if err := os.MkdirAll(skills, 0o755); err != nil {
					t.Fatalf("创建 skills 目录失败: %v", err)
				}
				if err := os.WriteFile(filepath.Join(skills, "README.md"), []byte("散置文件"), 0o644); err != nil {
					t.Fatalf("写入散置文件失败: %v", err)
				}
			},
			want: nil,
		},
		{
			name: "技能目录内文件名大小写不符被忽略",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".laxcode", "skills", "foo")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("创建技能目录失败: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "skill.md"), []byte(validSkillMD("foo", "描述")), 0o644); err != nil {
					t.Fatalf("写入 skill.md 失败: %v", err)
				}
			},
			want: nil,
		},
		{
			name: "有效与无效技能混存时仅加载有效者",
			setup: func(t *testing.T, root string) {
				writeSkillFile(t, root, "good", validSkillMD("good", "好技能"))
				writeSkillFile(t, root, "bad", "---\nname: bad\n---\n")
			},
			want: []Skill{{Name: "good", Description: "好技能"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)

			got := LoadSkills(root)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadSkills() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// captureStdout 捕获 fn 执行期间经 printer 默认实例输出的内容
// （LoadSkills 的警告走 printer 包级 Warnf 委托默认实例落笔）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	prev := printer.Default()
	printer.SetDefault(printer.NewWriterPrinter(w, printer.ColorGray, printer.ColorGreen))
	defer func() {
		printer.SetDefault(prev)
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("关闭管道失败: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取管道失败: %v", err)
	}
	return string(out)
}

func TestLoadSkillsInvalidSkillWarns(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "bad", "---\nname: bad\n---\n")
	writeSkillFile(t, root, "good", validSkillMD("good", "好技能"))

	out := captureStdout(t, func() {
		if skills := LoadSkills(root); len(skills) != 1 || skills[0].Name != "good" {
			t.Errorf("应只加载有效技能 good，实际 %+v", skills)
		}
	})
	if !strings.Contains(out, "跳过无效 skill") || !strings.Contains(out, "bad") {
		t.Errorf("期望输出跳过 bad 技能的警告，实际输出: %q", out)
	}
}

func TestLoadSkillsMissingSkillsDirSilent(t *testing.T) {
	root := t.TempDir()

	out := captureStdout(t, func() {
		if skills := LoadSkills(root); len(skills) != 0 {
			t.Errorf("skills 目录不存在时应返回空集合，实际 %+v", skills)
		}
	})
	if out != "" {
		t.Errorf("skills 目录不存在时应静默，实际输出: %q", out)
	}
}

func TestRenderSkillIndex(t *testing.T) {
	preamble := "## 可用技能（Skills）\n\n" +
		"以下是可用技能索引。技能不是工具，无法直接调用；当任务与某技能相关时，\n" +
		"先读取其定义文件 .laxcode/skills/<技能名>/SKILL.md，再按文件内容指引完成任务。\n" +
		"与任务无关的技能请忽略。\n\n"

	tests := []struct {
		name   string
		skills []Skill
		want   string
	}{
		{
			name:   "nil 技能集合返回空字符串",
			skills: nil,
			want:   "",
		},
		{
			name:   "空技能集合返回空字符串",
			skills: []Skill{},
			want:   "",
		},
		{
			name:   "渲染前言与单个条目",
			skills: []Skill{{Name: "commit", Description: "生成规范 commit message"}},
			want:   preamble + "- commit: 生成规范 commit message\n",
		},
		{
			name: "多个条目逐行渲染且 description 折叠单行",
			skills: []Skill{
				{Name: "commit", Description: "生成规范 commit message"},
				{Name: "pdf-tools", Description: "第一行\n第二行\t带制表"},
			},
			want: preamble +
				"- commit: 生成规范 commit message\n" +
				"- pdf-tools: 第一行 第二行 带制表\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderSkillIndex(tt.skills); got != tt.want {
				t.Errorf("RenderSkillIndex() =\n%q\nwant =\n%q", got, tt.want)
			}
		})
	}
}

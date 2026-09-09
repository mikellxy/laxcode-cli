package prompt

// 技能解析与校验的行为测试：全部经 fakeSkillSource 注入文件内容，不触磁盘。
// 「发现」层面的规则（skills 目录不存在、嵌套不递归、散置文件、文件名大小写
// 不符）属 SkillSource 实现的职责，用例见 infrastructure/skillrepo。

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeSkillSource 是 SkillSource 的内存替身。
type fakeSkillSource struct {
	files []SkillFile
	// gotWorkDirs 记录每次 List 收到的 workDir，用于断言端口按调用传参
	// （子 Agent 可能跑在与主 Agent 不同的工作目录下）
	gotWorkDirs []string
}

func (f *fakeSkillSource) List(workDir string) []SkillFile {
	f.gotWorkDirs = append(f.gotWorkDirs, workDir)
	return f.files
}

// warnRecorder 收集 LoadSkills 交出的跳过警告。
type warnRecorder struct{ msgs []string }

func (w *warnRecorder) fn() func(string) {
	return func(msg string) { w.msgs = append(w.msgs, msg) }
}

// validSkillMD 生成以 name/description 为 frontmatter 的合法 SKILL.md 内容。
func validSkillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n正文：按指引完成任务。\n", name, description, name)
}

func TestLoadSkills(t *testing.T) {
	tests := []struct {
		name string
		// files 是端口交出的技能定义文件（DirName + SKILL.md 全文）
		files []SkillFile
		want  []Skill
		// wantWarns 是期望的跳过警告条数
		wantWarns int
		// wantWarnContains 是每条期望出现在警告里的片段，用于守住规范要求的
		// 「给出修复方向」「包含命名规则说明」等内容契约
		wantWarnContains []string
	}{
		{
			// 无技能时必须返回 nil 而非空切片：调用方与测试都用
			// reflect.DeepEqual 与 nil 比较，返回 []Skill{} 会静默失配
			name: "无技能文件返回 nil",
		},
		{
			name:  "合法 frontmatter 通过",
			files: []SkillFile{{DirName: "pdf-tools", Content: validSkillMD("pdf-tools", "根据文档内容生成 PDF")}},
			want:  []Skill{{Name: "pdf-tools", Description: "根据文档内容生成 PDF"}},
		},
		{
			name: "description 含冒号时以引号包裹完整解析",
			files: []SkillFile{{DirName: "deploy",
				Content: "---\nname: deploy\ndescription: \"deploy: how to deploy the service\"\n---\n"}},
			want: []Skill{{Name: "deploy", Description: "deploy: how to deploy the service"}},
		},
		{
			name: "description 冒号后无空格时无需引号",
			files: []SkillFile{{DirName: "deploy2",
				Content: "---\nname: deploy2\ndescription: deploy:how to deploy\n---\n"}},
			want: []Skill{{Name: "deploy2", Description: "deploy:how to deploy"}},
		},
		{
			name: "多行 description 经 YAML 折叠语法解析",
			files: []SkillFile{{DirName: "long",
				Content: "---\nname: long\ndescription: >-\n  第一行\n  第二行\n---\n"}},
			want: []Skill{{Name: "long", Description: "第一行 第二行"}},
		},
		{
			name: "多个技能按 name 升序返回",
			files: []SkillFile{
				{DirName: "pdf-tools", Content: validSkillMD("pdf-tools", "根据文档内容生成 PDF")},
				{DirName: "commit", Content: validSkillMD("commit", "生成规范 commit message")},
				{DirName: "bash-helper", Content: validSkillMD("bash-helper", "bash 用法助手")},
			},
			want: []Skill{
				{Name: "bash-helper", Description: "bash 用法助手"},
				{Name: "commit", Description: "生成规范 commit message"},
				{Name: "pdf-tools", Description: "根据文档内容生成 PDF"},
			},
		},
		{
			name:             "空文件跳过",
			files:            []SkillFile{{DirName: "empty", Content: ""}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 empty 已被跳过", "文件为空"},
		},
		{
			name:             "无 frontmatter 跳过",
			files:            []SkillFile{{DirName: "plain", Content: "# 普通 Markdown\n没有 frontmatter\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 plain 已被跳过", "缺少 YAML frontmatter"},
		},
		{
			name:             "frontmatter 未闭合跳过",
			files:            []SkillFile{{DirName: "unclosed", Content: "---\nname: unclosed\ndescription: x\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 unclosed 已被跳过", "未闭合"},
		},
		{
			name:             "YAML 语法错误跳过（未闭合流式序列）",
			files:            []SkillFile{{DirName: "bad-yaml", Content: "---\nname: bad-yaml\ndescription: x\nextra: [unclosed\n---\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 bad-yaml 已被跳过", "不是合法 YAML"},
		},
		{
			name:             "未加引号的冒号空格构成 YAML 语法错误跳过",
			files:            []SkillFile{{DirName: "bad-colon", Content: "---\nname: bad-colon\ndescription: deploy: how to deploy\n---\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 bad-colon 已被跳过", "不是合法 YAML"},
		},
		{
			name:             "name 空白跳过",
			files:            []SkillFile{{DirName: "blank-name", Content: "---\nname: \"  \"\ndescription: x\n---\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 blank-name 已被跳过", "缺少非空白的 name 或 description"},
		},
		{
			name:             "description 缺失跳过",
			files:            []SkillFile{{DirName: "no-desc", Content: "---\nname: no-desc\n---\n"}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 no-desc 已被跳过", "缺少非空白的 name 或 description"},
		},
		{
			// 规范要求：身份不一致的警告须同时给出两种修复方向
			name:      "name 与目录名不一致跳过",
			files:     []SkillFile{{DirName: "pdf-tools", Content: validSkillMD("pdf", "描述")}},
			wantWarns: 1,
			wantWarnContains: []string{
				"技能 pdf-tools 已被跳过",
				"不一致",
				`请把 name 改为 "pdf-tools"`,
				`把目录重命名为 "pdf"`,
			},
		},
		{
			name:             "大写字母名称跳过",
			files:            []SkillFile{{DirName: "PdfTools", Content: validSkillMD("PdfTools", "描述")}},
			wantWarns:        1,
			wantWarnContains: []string{"技能 PdfTools 已被跳过", "不符合命名规则", skillNameRule},
		},
		{
			name:             "首尾连字符名称跳过",
			files:            []SkillFile{{DirName: "-pdf-", Content: validSkillMD("-pdf-", "描述")}},
			wantWarns:        1,
			wantWarnContains: []string{"不符合命名规则", skillNameRule},
		},
		{
			name:             "连续连字符名称跳过",
			files:            []SkillFile{{DirName: "pdf--tools", Content: validSkillMD("pdf--tools", "描述")}},
			wantWarns:        1,
			wantWarnContains: []string{"不符合命名规则", skillNameRule},
		},
		{
			name:             "隐藏目录名称跳过",
			files:            []SkillFile{{DirName: ".hidden", Content: validSkillMD(".hidden", "描述")}},
			wantWarns:        1,
			wantWarnContains: []string{"不符合命名规则", skillNameRule},
		},
		{
			name: "名称超过 64 字符跳过",
			files: []SkillFile{{
				DirName: strings.Repeat("a", skillNameMaxLen+1),
				Content: validSkillMD(strings.Repeat("a", skillNameMaxLen+1), "描述"),
			}},
			wantWarns: 1,
			// 规范要求：命名不合规的警告须包含命名规则说明
			wantWarnContains: []string{"超过上限 64", skillNameRule},
		},
		{
			name: "有效与无效技能混存时仅加载有效者",
			files: []SkillFile{
				{DirName: "good", Content: validSkillMD("good", "好技能")},
				{DirName: "bad", Content: "---\nname: bad\n---\n"},
			},
			want:      []Skill{{Name: "good", Description: "好技能"}},
			wantWarns: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &fakeSkillSource{files: tt.files}
			var rec warnRecorder

			got := LoadSkills(src, "/any/workdir", rec.fn())
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadSkills() = %+v, want %+v", got, tt.want)
			}
			if len(rec.msgs) != tt.wantWarns {
				t.Fatalf("跳过警告条数 = %d（%+v）, want %d", len(rec.msgs), rec.msgs, tt.wantWarns)
			}
			for _, sub := range tt.wantWarnContains {
				if !strings.Contains(rec.msgs[0], sub) {
					t.Errorf("警告应包含 %q，实际: %q", sub, rec.msgs[0])
				}
			}
		})
	}
}

func TestLoadSkillsNilSourceReturnsNil(t *testing.T) {
	var rec warnRecorder
	if got := LoadSkills(nil, "/any/workdir", rec.fn()); got != nil {
		t.Errorf("src 为 nil 时应返回 nil，实际 %+v", got)
	}
	if len(rec.msgs) != 0 {
		t.Errorf("src 为 nil 时不应产生警告，实际 %+v", rec.msgs)
	}
}

// TestLoadSkillsPassesWorkDirToSource 守住端口的参数化契约：workDir 按调用传入
// 而非构造期绑定，同一个实例才能同时服务主 Agent 与不同工作目录的子 Agent。
func TestLoadSkillsPassesWorkDirToSource(t *testing.T) {
	src := &fakeSkillSource{files: []SkillFile{{DirName: "a", Content: validSkillMD("a", "d")}}}

	LoadSkills(src, "/first", nil)
	LoadSkills(src, "/second", nil)

	want := []string{"/first", "/second"}
	if !reflect.DeepEqual(src.gotWorkDirs, want) {
		t.Errorf("List 收到的 workDir = %v, want %v", src.gotWorkDirs, want)
	}
}

// TestLoadSkillsNilWarnStaysSilent 验证 warn 为 nil 时既不 panic 也不影响加载：
// 子 Agent 即以此方式复用同一份技能发现端口而不重复刷警告。
func TestLoadSkillsNilWarnStaysSilent(t *testing.T) {
	src := &fakeSkillSource{files: []SkillFile{
		{DirName: "bad", Content: "---\nname: bad\n---\n"},
		{DirName: "good", Content: validSkillMD("good", "好技能")},
	}}

	got := LoadSkills(src, "/any", nil)
	want := []Skill{{Name: "good", Description: "好技能"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadSkills() = %+v, want %+v", got, want)
	}
}

func TestParseSkill(t *testing.T) {
	t.Run("合法内容返回技能且原因为空", func(t *testing.T) {
		skill, reason := parseSkill(validSkillMD("commit", "生成规范 commit message"), "commit")
		if reason != "" {
			t.Errorf("合法技能不应有跳过原因，实际 %q", reason)
		}
		if skill.Name != "commit" || skill.Description != "生成规范 commit message" {
			t.Errorf("parseSkill() = %+v", skill)
		}
	})

	t.Run("未知 frontmatter 字段被忽略（向前兼容）", func(t *testing.T) {
		content := "---\nname: fwd\ndescription: d\nallowed-tools: [bash]\n---\n"
		skill, reason := parseSkill(content, "fwd")
		if reason != "" {
			t.Errorf("未知字段不应导致跳过，实际 %q", reason)
		}
		if skill.Name != "fwd" {
			t.Errorf("parseSkill() = %+v", skill)
		}
	})

	t.Run("BOM 与首行空白不影响 frontmatter 识别", func(t *testing.T) {
		content := "\ufeff  ---\nname: bom\ndescription: d\n---\n"
		if _, reason := parseSkill(content, "bom"); reason != "" {
			t.Errorf("带 BOM 的合法文件不应被跳过，实际 %q", reason)
		}
	})

	t.Run("校验顺序：身份一致性先于命名规范", func(t *testing.T) {
		// 目录名与 name 同为非法大写：应先报身份一致之外的命名规则，
		// 而目录名合法但 name 非法大写时，身份校验先命中
		_, reason := parseSkill(validSkillMD("Pdf", "d"), "pdf-tools")
		if !strings.Contains(reason, "不一致") {
			t.Errorf("name 与目录名不一致应先于命名规范被报出，实际 %q", reason)
		}
	})
}

func TestExtractFrontmatter(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantFM     string
		wantReason string
	}{
		{name: "空文件", content: "", wantReason: "文件为空"},
		{name: "首行非分隔符", content: "# title\n", wantReason: "首行须为 ---"},
		{name: "未闭合", content: "---\nname: x\n", wantReason: "未闭合"},
		{name: "正常闭合", content: "---\nname: x\n---\n正文\n", wantFM: "name: x"},
		{name: "分隔行含空白仍算闭合", content: "---\nname: x\n  ---  \n", wantFM: "name: x"},
		{name: "空 frontmatter", content: "---\n---\n", wantFM: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, reason := extractFrontmatter(tt.content)
			if tt.wantReason != "" {
				if !strings.Contains(reason, tt.wantReason) {
					t.Errorf("reason = %q, want it to contain %q", reason, tt.wantReason)
				}
				return
			}
			if reason != "" {
				t.Fatalf("不应失败，reason = %q", reason)
			}
			if fm != tt.wantFM {
				t.Errorf("fmText = %q, want %q", fm, tt.wantFM)
			}
		})
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

func TestCollapseWhitespace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"a\nb", "a b"},
		{"a\t\tb", "a b"},
		{"  a b  ", "a b"},
	}
	for _, tt := range tests {
		if got := collapseWhitespace(tt.in); got != tt.want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

package prompt

// 系统提示词拼装的行为测试：技能经内存替身注入，Plan Mode 的会话目录直接给定，
// 全程不触磁盘——磁盘布局属 infrastructure/layout 的职责，组合根算好后注入。

import (
	"path/filepath"
	"strings"
	"testing"
)

// testWorkDir 是提示词拼装用的工作目录取值；本包测试不访问文件系统，
// 只需一个能被原样插值进人格模板的可辨识字符串。
const testWorkDir = "/tmp/lax-proj"

// testSessionDir 是 Plan Mode 规划文件的落盘目录，等价于组合根调用
// layout.SessionDir(testWorkDir, "sess-abc") 的结果。
var testSessionDir = filepath.Join(testWorkDir, ".laxcode", ".session", "sess-abc")

func TestGetSysPromptPersonalityAndWorkDir(t *testing.T) {
	out := GetSysPrompt(testWorkDir, nil, nil)

	// 人格提示词必须出现且 %s 占位被 workDir 填充（沙箱约束依赖它）
	if !strings.Contains(out, testWorkDir) {
		t.Errorf("系统提示应包含工作目录 %q，实际输出:\n%s", testWorkDir, out)
	}
	if !strings.Contains(out, "【沙箱强制约束】") {
		t.Errorf("系统提示应包含人格模板正文")
	}
	// plan 传 nil 时不应出现 Plan Mode 规划说明，调用方无需为会话目录编造取值
	if strings.Contains(out, "Plan Mode") || strings.Contains(out, "plan.md") {
		t.Errorf("plan 为 nil 时不应包含 Plan Mode 工作流，实际输出:\n%s", out)
	}
	if strings.Contains(out, "%s") {
		t.Errorf("模板占位符应全部被替换，实际输出:\n%s", out)
	}
}

func TestGetSysPromptPlanMode(t *testing.T) {
	out := GetSysPrompt(testWorkDir, nil, &PlanMode{SessionDir: testSessionDir})

	if !strings.Contains(out, "Plan Mode") {
		t.Errorf("plan 非 nil 时应包含 Plan Mode 段落")
	}
	// 注入的会话规划目录须被填进模板（模型据此决定 plan.md/design.md 落盘位置）
	if !strings.Contains(out, testSessionDir) {
		t.Errorf("Plan Mode 提示应包含会话规划目录 %q，实际输出:\n%s", testSessionDir, out)
	}
	if !strings.Contains(out, "plan.md") || !strings.Contains(out, "design.md") {
		t.Errorf("Plan Mode 提示应包含 plan.md/design.md 工作流说明")
	}
}

func TestGetSysPromptIncludesSkillIndex(t *testing.T) {
	skills := LoadSkills(&fakeSkillSource{files: []SkillFile{
		{DirName: "pdf-tools", Content: validSkillMD("pdf-tools", "根据文档内容生成 PDF")},
	}}, testWorkDir, nil)

	out := GetSysPrompt(testWorkDir, skills, nil)
	if !strings.Contains(out, "## 可用技能（Skills）") {
		t.Errorf("存在合法技能时应渲染技能索引段，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "- pdf-tools: 根据文档内容生成 PDF") {
		t.Errorf("技能索引应包含 pdf-tools 条目，实际输出:\n%s", out)
	}
}

func TestGetSysPromptNoSkillsNoIndex(t *testing.T) {
	tests := []struct {
		name   string
		skills []Skill
	}{
		{name: "nil 技能集合", skills: nil},
		{name: "空技能集合", skills: []Skill{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := GetSysPrompt(testWorkDir, tt.skills, nil)
			if strings.Contains(out, "## 可用技能（Skills）") {
				t.Errorf("无技能时应省略技能索引段，实际输出:\n%s", out)
			}
		})
	}
}

func TestGetSysPromptInvalidSkillsDoNotBreakPrompt(t *testing.T) {
	// 无效技能与有效技能混存：无效者被跳过，系统提示仍正常生成且不含无效条目
	skills := LoadSkills(&fakeSkillSource{files: []SkillFile{
		{DirName: "bad", Content: "---\nname: bad\n---\n"},
		{DirName: "good", Content: validSkillMD("good", "好技能")},
	}}, testWorkDir, nil)

	out := GetSysPrompt(testWorkDir, skills, nil)
	if !strings.Contains(out, "- good: 好技能") {
		t.Errorf("应渲染有效技能条目 good，实际输出:\n%s", out)
	}
	if strings.Contains(out, "- bad:") {
		t.Errorf("无效技能 bad 不应出现在技能索引中，实际输出:\n%s", out)
	}
}

// TestGetSysPromptSectionOrder 守住段落顺序契约：人格 → 技能索引 → Plan Mode。
// 顺序是提示词工程的一部分（越靠前权重越高），不可因拼装重构被无意调换。
func TestGetSysPromptSectionOrder(t *testing.T) {
	skills := []Skill{{Name: "commit", Description: "生成规范 commit message"}}
	out := GetSysPrompt(testWorkDir, skills, &PlanMode{SessionDir: testSessionDir})

	personalityAt := strings.Index(out, "【沙箱强制约束】")
	skillsAt := strings.Index(out, "## 可用技能（Skills）")
	planAt := strings.Index(out, "Plan Mode")
	if personalityAt < 0 || skillsAt < 0 || planAt < 0 {
		t.Fatalf("三段都应出现，实际 personality=%d skills=%d plan=%d", personalityAt, skillsAt, planAt)
	}
	if personalityAt >= skillsAt || skillsAt >= planAt {
		t.Errorf("段落顺序应为 人格 → 技能索引 → Plan Mode，实际位置 %d/%d/%d",
			personalityAt, skillsAt, planAt)
	}
}

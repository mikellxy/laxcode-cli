package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillFileForSys 在 root/.laxcode/skills/<dirName>/ 下写入合法 SKILL.md。
func writeSkillFileForSys(t *testing.T, root, dirName, content string) {
	t.Helper()
	dir := filepath.Join(root, ".laxcode", "skills", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md 失败: %v", err)
	}
}

func TestGetSysPromptPersonalityAndWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "proj")

	out := GetSysPrompt(workDir, "sess-1", false)
	// 人格提示词必须出现且 %s 占位被 workDir 填充（沙箱约束依赖它）
	if !strings.Contains(out, workDir) {
		t.Errorf("系统提示应包含工作目录 %q，实际输出:\n%s", workDir, out)
	}
	if !strings.Contains(out, "【沙箱强制约束】") {
		t.Errorf("系统提示应包含人格模板正文")
	}
	// 未启用 plan mode 时不应出现 Plan Mode 规划说明
	if strings.Contains(out, "任务规划") || strings.Contains(out, "plan.md") {
		t.Errorf("planMode=false 时不应包含 Plan Mode 工作流，实际输出:\n%s", out)
	}
}

func TestGetSysPromptPlanMode(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "proj")
	sessID := "sess-abc"

	out := GetSysPrompt(workDir, sessID, true)
	if !strings.Contains(out, "Plan Mode") {
		t.Errorf("planMode=true 时应包含 Plan Mode 段落")
	}
	// 会话规划目录 = workDir/.laxcode/.session/<sessID>
	sessDir := filepath.Join(workDir, ".laxcode", ".session", sessID)
	if !strings.Contains(out, sessDir) {
		t.Errorf("Plan Mode 提示应包含会话规划目录 %q，实际输出:\n%s", sessDir, out)
	}
	if !strings.Contains(out, "plan.md") || !strings.Contains(out, "design.md") {
		t.Errorf("Plan Mode 提示应包含 plan.md/design.md 工作流说明")
	}
}

func TestGetSysPromptIncludesSkillIndex(t *testing.T) {
	root := t.TempDir()
	writeSkillFileForSys(t, root, "pdf-tools",
		validSkillMD("pdf-tools", "根据文档内容生成 PDF"))

	out := GetSysPrompt(root, "sess-1", false)
	if !strings.Contains(out, "## 可用技能（Skills）") {
		t.Errorf("工作目录含合法技能时应渲染技能索引段，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "- pdf-tools: 根据文档内容生成 PDF") {
		t.Errorf("技能索引应包含 pdf-tools 条目，实际输出:\n%s", out)
	}
}

func TestGetSysPromptNoSkillsNoIndex(t *testing.T) {
	root := t.TempDir()
	out := GetSysPrompt(root, "sess-1", false)
	if strings.Contains(out, "## 可用技能（Skills）") {
		t.Errorf("无技能时应省略技能索引段，实际输出:\n%s", out)
	}
}

func TestGetSysPromptInvalidSkillsDoNotBreakPrompt(t *testing.T) {
	root := t.TempDir()
	// 无效技能与有效技能混存：无效者被静默跳过，系统提示仍正常生成
	writeSkillFileForSys(t, root, "bad", "---\nname: bad\n---\n")
	writeSkillFileForSys(t, root, "good", validSkillMD("good", "好技能"))

	out := GetSysPrompt(root, "sess-1", false)
	if !strings.Contains(out, "- good: 好技能") {
		t.Errorf("应渲染有效技能条目 good，实际输出:\n%s", out)
	}
	if strings.Contains(out, "- bad:") {
		t.Errorf("无效技能 bad 不应出现在技能索引中，实际输出:\n%s", out)
	}
}

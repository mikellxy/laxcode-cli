// Package layout 是 LaxCode 磁盘布局的单一真源（single source of truth）。
//
// 在此之前，".laxcode"、".session"、"skills"、"log/tracing.log" 这些相对路径
// 片段散落在组合根（cmd/agentasm）、领域提示词（domain/prompt）与基础设施
// 适配器（infrastructure/config）中被各自硬编码：同一条布局规则有三份副本，
// 改一处即静默失配。本包把它们收口为一组纯函数，任何需要落盘位置的调用方
// 一律经此拼装。
//
// 两个不同的根共用 RootDirName：
//   - 工作目录根 ${workDir}/.laxcode —— 会话数据与技能定义（随项目走）
//   - 用户主目录根 ${home}/.laxcode —— settings.json（随用户走）
//
// 仍有两处“非代码”的布局副本无法由本包约束，改动布局时须同步：
//   - prompt.RenderSkillIndex 向模型描述技能定义文件的相对路径
//   - scripts/clean_session.sh 清理会话目录
package layout

import "path/filepath"

const (
	// RootDirName 是 LaxCode 的数据根目录名，工作目录与用户主目录下同名。
	RootDirName = ".laxcode"

	// SessionDirName 是会话数据目录名，其下每个会话一个子目录。
	SessionDirName = ".session"

	// SkillsDirName 是技能定义目录名，其下恰好一层子目录各含一个 SKILL.md。
	SkillsDirName = "skills"

	// settingsFileName 是用户主目录数据根下的配置文件名。
	settingsFileName = "settings.json"

	// tracingLogDirName 与 tracingLogFileName 定位会话级 trace 日志。
	tracingLogDirName  = "log"
	tracingLogFileName = "tracing.log"
)

// Root 返回工作目录下的数据根 ${workDir}/.laxcode。
func Root(workDir string) string {
	return filepath.Join(workDir, RootDirName)
}

// SessionRoot 返回会话数据根 ${workDir}/.laxcode/.session，
// 可直接作为 sessionrepo.NewFsSessionRepo 的入参。
func SessionRoot(workDir string) string {
	return filepath.Join(Root(workDir), SessionDirName)
}

// SessionDir 返回单个会话的数据目录 ${workDir}/.laxcode/.session/${sessID}，
// Plan Mode 的规划文件（plan.md / design.md 等）即落在这里。
func SessionDir(workDir, sessID string) string {
	return filepath.Join(SessionRoot(workDir), sessID)
}

// TracingLog 返回会话级 trace 日志文件
// ${workDir}/.laxcode/.session/${sessID}/log/tracing.log。
func TracingLog(workDir, sessID string) string {
	return filepath.Join(SessionDir(workDir, sessID), tracingLogDirName, tracingLogFileName)
}

// SkillsRoot 返回技能定义根 ${workDir}/.laxcode/skills，
// 其发现规则见 infrastructure/skillrepo。
func SkillsRoot(workDir string) string {
	return filepath.Join(Root(workDir), SkillsDirName)
}

// UserSettings 返回用户级配置文件 ${homeDir}/.laxcode/settings.json。
// 与工作目录布局共用 RootDirName 但根不同，故单列一个函数而非复用 Root。
func UserSettings(homeDir string) string {
	return filepath.Join(homeDir, RootDirName, settingsFileName)
}

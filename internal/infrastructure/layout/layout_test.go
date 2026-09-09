package layout

// 布局函数的行为测试：把每条路径的拼装结果逐个钉死。这些字符串是磁盘契约，
// 改动即意味着既有会话数据与技能目录失联（用户的 .laxcode/.session 历史读不
// 回来），所以宁可写几条看起来"显而易见"的断言，也不让它被无意改掉。

import (
	"path/filepath"
	"testing"
)

// workDir 是断言用的工作目录取值；本包纯拼装路径，不访问文件系统。
const workDir = "/wd"

func TestDirNames(t *testing.T) {
	// 目录名即磁盘契约：改名会让既有数据失联，故显式钉死
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"RootDirName", RootDirName, ".laxcode"},
		{"SessionDirName", SessionDirName, ".session"},
		{"SkillsDirName", SkillsDirName, "skills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestPaths(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "Root",
			got:  Root(workDir),
			want: filepath.Join(workDir, ".laxcode"),
		},
		{
			name: "SessionRoot",
			got:  SessionRoot(workDir),
			want: filepath.Join(workDir, ".laxcode", ".session"),
		},
		{
			name: "SessionDir",
			got:  SessionDir(workDir, "sess-1"),
			want: filepath.Join(workDir, ".laxcode", ".session", "sess-1"),
		},
		{
			name: "TracingLog",
			got:  TracingLog(workDir, "sess-1"),
			want: filepath.Join(workDir, ".laxcode", ".session", "sess-1", "log", "tracing.log"),
		},
		{
			name: "SkillsRoot",
			got:  SkillsRoot(workDir),
			want: filepath.Join(workDir, ".laxcode", "skills"),
		},
		{
			name: "UserSettings",
			got:  UserSettings("/home/u"),
			want: filepath.Join("/home/u", ".laxcode", "settings.json"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestHierarchy 守住各路径之间的层级关系：sessionrepo 以 SessionRoot 为根、
// 以 sessID 为子目录名读写历史与元信息，Plan Mode 与 trace 日志都落在同一个
// 会话目录内——层级一错，会话数据与规划文件就会各写一处。
func TestHierarchy(t *testing.T) {
	sessDir := SessionDir(workDir, "sess-1")

	if got := filepath.Dir(sessDir); got != SessionRoot(workDir) {
		t.Errorf("SessionDir 的父目录 = %q, want SessionRoot = %q", got, SessionRoot(workDir))
	}
	if got := filepath.Dir(SessionRoot(workDir)); got != Root(workDir) {
		t.Errorf("SessionRoot 的父目录 = %q, want Root = %q", got, Root(workDir))
	}
	if got := filepath.Dir(SkillsRoot(workDir)); got != Root(workDir) {
		t.Errorf("SkillsRoot 的父目录 = %q, want Root = %q", got, Root(workDir))
	}
	if got := filepath.Dir(TracingLog(workDir, "sess-1")); got != filepath.Join(sessDir, "log") {
		t.Errorf("TracingLog 的所在目录 = %q, want %q", got, filepath.Join(sessDir, "log"))
	}
}

// TestUserSettingsSitsInHomeRoot 守住两个根的区分：用户级配置直接躺在主目录的
// .laxcode 根下（随用户走），不得与会话/技能数据混在同一层（随项目走）。
func TestUserSettingsSitsInHomeRoot(t *testing.T) {
	const home = "/home/u"

	if got, want := filepath.Dir(UserSettings(home)), Root(home); got != want {
		t.Errorf("settings.json 所在目录 = %q, want %q", got, want)
	}
	if got := filepath.Base(UserSettings(home)); got != "settings.json" {
		t.Errorf("配置文件名 = %q, want %q", got, "settings.json")
	}
	if UserSettings(home) == SessionRoot(home) || UserSettings(home) == SkillsRoot(home) {
		t.Errorf("用户级配置路径不得与会话/技能数据路径重合: %q", UserSettings(home))
	}
}

package agentasm

// 架构约束测试：把 README「7. 架构」与 openspec/config.yaml 里写下的分层规则
// 变成会失败的断言。这些规则原本只能靠 review 维持——一旦有人图省事在 domain
// 里直接 os.Open，或在 application 里 import infrastructure，文档与代码就会悄悄
// 分叉，而分叉的代价要等到下一次跨平台编译失败或换观测后端时才显现。
//
// 放在 cmd/agentasm 有两个原因：组合根正是这些装配规则的责任方，且它已同时
// 依赖三层。检查基于源码 import 而非 go list，因此不受构建标签影响（unix 与
// 非 unix 文件一并解析，对架构约束来说这样更保守）。

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// modulePath 是本仓库的 module 前缀，用于把内部包 import 与第三方区分开。
const modulePath = "github.com/mikellxy/laxcode/"

// layers 是被约束的源码树；internal/infrastructure 也在列，因为它同样不得
// 反向依赖上层之外的东西（如硬编码磁盘布局片段）。
var layers = []string{"cmd", "internal/domain", "internal/application", "internal/infrastructure"}

// repoRoot 从本测试的工作目录（cmd/agentasm）回溯到仓库根，并用 go.mod 校验。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s 下没有 go.mod，仓库根定位错误：%v", root, err)
	}
	return root
}

// scanImports 收集 rel 目录下每个包（目录）的非测试文件 import 路径。
// 测试文件被排除：application 层的测试会装配真实适配器（如 skillrepo），
// 那是组合根职责在测试里的复刻，不构成生产依赖。
func scanImports(t *testing.T, rel string) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	out := make(map[string][]string)
	fset := token.NewFileSet()
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isProdGoFile(path) {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		dir, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(dir)
		for _, imp := range f.Imports {
			out[key] = append(out[key], strings.Trim(imp.Path.Value, `"`))
		}
		return nil
	}
	if err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(rel)), walk); err != nil {
		t.Fatalf("扫描 %s 失败：%v", rel, err)
	}
	return out
}

// scanAllImports 把 layers 下所有包的非测试 import 汇总成 目录 → import 列表。
func scanAllImports(t *testing.T) map[string][]string {
	t.Helper()
	all := make(map[string][]string)
	for _, layer := range layers {
		for dir, imports := range scanImports(t, layer) {
			all[dir] = append(all[dir], imports...)
		}
	}
	return all
}

func isProdGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

// isInside 判断目录 dir 是否等于 p 或位于 p 之下（按路径段比较，避免
// "internal/domain/tools" 误命中前缀 "internal/domain/tool"）。
func isInside(dir string, parents []string) bool {
	for _, p := range parents {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// 依赖方向：domain 与 application 都不得 import infrastructure。需要 OS 能力时
// 在 domain 定义端口、infrastructure 实现、由本包装配。
func TestDomainAndApplicationDoNotImportInfrastructure(t *testing.T) {
	const infraPrefix = modulePath + "internal/infrastructure"
	for _, layer := range []string{"internal/domain", "internal/application"} {
		for dir, imports := range scanImports(t, layer) {
			for _, imp := range imports {
				if strings.HasPrefix(imp, infraPrefix) {
					t.Errorf("%s 依赖了基础设施层 %s：应在 domain 定义端口接口、由 infrastructure 实现、cmd/agentasm 装配", dir, imp)
				}
			}
		}
	}
}

// osMechanics 是 domain 不得直接使用的 OS 机制包：它们回答"怎么做"（打开文件、
// 起进程、发信号、解析工作目录），属基础设施；domain 只回答"做什么"（读第几页、
// 超时归为哪类错误、路径不得越出 work_dir）。
var osMechanics = []string{"os", "os/exec", "syscall", "path/filepath"}

// osMechanicsAllowlist 是唯一豁免：domain/tools 用 path/filepath 实现
// safeJoinWorkDir 的工作目录沙箱校验（Abs/Clean/Rel）。"工具不得越出 work_dir"
// 本身是领域安全策略，故留在 domain 是有意为之；filepath.Abs 对相对路径会调
// os.Getwd，这是该策略必须付出的代价。新增豁免须在此写明理由。
var osMechanicsAllowlist = map[string][]string{
	"internal/domain/tools": {"path/filepath"},
}

func TestDomainDoesNotImportOSMechanics(t *testing.T) {
	imports := scanImports(t, "internal/domain")
	if len(imports) == 0 {
		t.Fatal("未扫描到 internal/domain 下任何包，检查是否失效")
	}
	for dir, imps := range imports {
		for _, imp := range imps {
			if !slices.Contains(osMechanics, imp) || slices.Contains(osMechanicsAllowlist[dir], imp) {
				continue
			}
			t.Errorf("%s 直接依赖 OS 机制包 %s：应抽端口交 infrastructure 实现（豁免清单见 osMechanicsAllowlist）", dir, imp)
		}
	}
}

// domain 的内部依赖必须收敛在 domain 之内。本文件其余用例只禁止“domain 依赖
// infrastructure / OS 机制”，对 internal/utils 这类层外杂项包无能为力（它根本
// 不在 layers 里）：一旦 domain 引了它，既绕过守护，又会让同一份纯算法（如
// token 估算）在两个包里各存一份。共享类型与纯算法的正确落点是
// domain/sharedkernel，跨层能力则在 domain 定义端口、交 infrastructure 实现。
func TestDomainInternalImportsStayInDomain(t *testing.T) {
	imports := scanImports(t, "internal/domain")
	if len(imports) == 0 {
		t.Fatal("未扫描到 internal/domain 下任何包，检查是否失效")
	}
	const domainPrefix = modulePath + "internal/domain/"
	for dir, imps := range imports {
		for _, imp := range imps {
			// 标准库与第三方不在此约束内（第三方依赖另有专门用例与 README 白名单）
			if !strings.HasPrefix(imp, modulePath) {
				continue
			}
			if !strings.HasPrefix(imp, domainPrefix) {
				t.Errorf("%s 依赖了领域层之外的内部包 %s：共享类型/纯算法请放 domain/sharedkernel，跨层能力请在 domain 定义端口交 infrastructure 实现", dir, imp)
			}
		}
	}
}

// otelHomes 是全仓唯一允许 import OpenTelemetry 的两处：domain/telemetry 承载
// 埋点语义（span 名、属性键、追踪辅助函数），infrastructure/tracing 承载
// TracerProvider 装配与导出实现（含 filetrace）。多一处，"换观测方案只改两个包"
// 就是空话。
var otelHomes = []string{"internal/domain/telemetry", "internal/infrastructure/tracing"}

func TestOtelImportsStayInTelemetryAndTracing(t *testing.T) {
	for dir, imports := range scanAllImports(t) {
		if isInside(dir, otelHomes) {
			continue
		}
		for _, imp := range imports {
			if strings.HasPrefix(imp, "go.opentelemetry.io/") {
				t.Errorf("%s 直接 import 了 %s：埋点请经 domain/telemetry，装配请经 infrastructure/tracing", dir, imp)
			}
		}
	}
}

// diskLayoutLiterals 是 .laxcode 磁盘布局的路径片段，以 Go 字符串字面量的形式
// 匹配（带引号，故不会误命中提示词模板里的说明性路径）。它们只允许出现在
// infrastructure/layout：那是布局的单一真源。散落多份的后果是改一处忘一处——
// 会话写到 A 目录、系统提示词却告诉模型 B 目录。
var diskLayoutLiterals = []string{`".laxcode"`, `".session"`, `"sessions.db"`, `"tracing.log"`, `"settings.json"`, `"skills"`}

const layoutHome = "internal/infrastructure/layout"

func TestDiskLayoutLiteralsStayInLayoutPkg(t *testing.T) {
	root := repoRoot(t)
	home := filepath.Join(root, filepath.FromSlash(layoutHome))
	var scanned int
	for _, layer := range layers {
		base := filepath.Join(root, filepath.FromSlash(layer))
		walk := func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !isProdGoFile(path) || strings.HasPrefix(path, home+string(filepath.Separator)) {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			for _, lit := range diskLayoutLiterals {
				if strings.Contains(string(src), lit) {
					t.Errorf("%s 硬编码了磁盘布局片段 %s：请改用 %s 的路径函数", filepath.ToSlash(rel), lit, layoutHome)
				}
			}
			return nil
		}
		if err := filepath.WalkDir(base, walk); err != nil {
			t.Fatalf("扫描 %s 失败：%v", layer, err)
		}
	}
	if scanned == 0 {
		t.Fatal("未扫描到任何源文件，检查是否失效")
	}
}

// 端口 → 适配器 的对应关系（README 表格）由本包装配，故在这里断言装配确实发生：
// 任何一个适配器没被接上，对应的领域能力就会在运行时静默退化（技能索引为空、
// 会话不落盘）。
func TestCompositionRootWiresEveryAdapter(t *testing.T) {
	imports := scanImports(t, "cmd/agentasm")["cmd/agentasm"]
	want := []string{
		modulePath + "internal/infrastructure/artifactstore",
		modulePath + "internal/infrastructure/sessionrepo",
		modulePath + "internal/infrastructure/llmprovider",
		modulePath + "internal/infrastructure/workfs",
		modulePath + "internal/infrastructure/shell",
		modulePath + "internal/infrastructure/skillrepo",
		modulePath + "internal/infrastructure/layout",
	}
	for _, w := range want {
		if !slices.Contains(imports, w) {
			t.Errorf("组合根未装配适配器 %s：对应端口将没有实现", w)
		}
	}
}

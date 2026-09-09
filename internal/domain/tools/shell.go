package tools

// bash 工具的命令执行端口：领域层只描述「在某工作目录跑一条命令、拿回归一
// 结果、运行结束可回收遗留后台进程」，而进程组、信号、输出临时文件等 OS 机制
// 由基础设施实现（internal/infrastructure/shell）。这样领域层不含 syscall，
// 可跨平台编译，命令执行的编排与面向模型的错误文案仍留在领域内。

import (
	"context"
	"time"
)

// ShellOutcome 是一次 shell 命令执行的归一结果，仅在命令确实跑完时有意义。
type ShellOutcome struct {
	// Output 是 stdout 与 stderr 合并后的原始输出，未截断。
	Output string
	// ExitCode 是命令退出码，正常结束为 0。
	ExitCode int
	// ExitErr 是命令未正常结束时的原始错误描述（如 "exit status 3"、
	// "signal: killed"）；退出码为 0 时为空串。领域层据此拼面向模型的
	// 失败说明，故保留实现方的原文而非只给退出码。
	ExitErr string
}

// ShellRunner 执行 shell 命令并管理其派生进程的生命周期。
//
// 实现须满足：
//   - 命令派生的后台进程不得阻塞 Run 返回；
//   - 超时或取消时收割该命令派生的整棵进程树，且不波及其他命令留下的后台进程；
//   - Close 回收本 Runner 生命周期内所有遗留的后台进程与中间产物；
//   - 命令以非零退出码结束时不返回 error，而是填入 ShellOutcome；
//     仅在超时/取消、无法启动、无法读取输出等基础设施失败时返回 error，
//     且超时/取消返回的 error 须满足 errors.Is(err, context.DeadlineExceeded)
//     或 errors.Is(err, context.Canceled)，供领域层区分文案。
type ShellRunner interface {
	// Run 在 workDir 下执行 command，至多等待 timeout；timeout <= 0 时取实现默认值。
	Run(ctx context.Context, workDir, command string, timeout time.Duration) (ShellOutcome, error)
	// Close 回收 Run 遗留的后台进程与中间产物，可安全重复调用。
	Close() error
}

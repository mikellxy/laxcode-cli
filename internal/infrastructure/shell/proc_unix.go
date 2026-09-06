//go:build unix

package shell

// POSIX 平台的进程组语义：让每条命令自成一个进程组，超时时按负 pid 收割
// 整棵进程树（含后台派生），且不波及其他命令留下的后台进程。

import (
	"os/exec"
	"syscall"
)

// setProcGroup 让子进程独立成组（子进程即组长，故 pgid == 子进程 pid）。
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// installCancelKill 覆盖 CommandContext 默认的"只杀直接子进程"，改为杀整组。
// 必须在 Start 之前完成赋值：os/exec 的 watchCtx 协程在 Start 返回后
// 可能并发读取 Cancel，Start 之后再写会构成数据竞争。闭包内对
// cmd.Process 的读取发生在取消时刻（彼时 Start 早已返回并赋好值）。
func installCancelKill(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return terminatePID(cmd.Process.Pid)
	}
}

// terminatePID 按负 pid 终止 pid 所领导的整个进程组；组已随命令正常退出时
// 返回 ESRCH，属预期，由调用方忽略。
func terminatePID(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

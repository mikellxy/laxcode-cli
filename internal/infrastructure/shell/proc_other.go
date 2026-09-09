//go:build !unix

package shell

// 非 POSIX 平台（Windows 等）没有进程组语义：退化为 os/exec 默认的
// "只终止直接子进程"。命令自身派生的后台进程需由命令负责清理，Close 只能
// 兜住直接子进程——这是平台限制，不是实现缺陷。保留本文件使领域层与
// 基础设施在任意 GOOS 下均可编译。

import (
	"os"
	"os/exec"
)

// setProcGroup 在无进程组语义的平台上为空操作。
func setProcGroup(*exec.Cmd) {}

// installCancelKill 保持 os/exec 默认行为（取消时终止直接子进程），无需覆盖。
func installCancelKill(*exec.Cmd) {}

// terminatePID 终止单个进程；进程已退出时返回错误，由调用方按预期忽略。
// 非 unix 平台的 os.FindProcess 总是成功，真正的失败发生在 Kill。
func terminatePID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

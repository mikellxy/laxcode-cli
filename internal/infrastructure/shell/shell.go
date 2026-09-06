// Package shell 实现 domain/tools.ShellRunner：bash 工具全部 OS 机制的落地点。
// 输出落临时文件而非管道、独立进程组收割、后台进程与临时文件回收都在此，
// 领域层只见到归一后的 ShellOutcome，因而不再含 syscall 且可跨平台编译。
// 平台差异（进程组语义）隔离在 proc_unix.go / proc_other.go。
//
// 路径安全：临时文件名由 os.CreateTemp 生成，不受模型入参影响；workDir
// 来自组合根（CLI 参数 / cwd），命令相对路径的沙箱校验属领域层职责。
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

const (
	// fallbackTimeout 是端口契约中 timeout<=0 时的兜底上限。正常路径由领域层
	// 的 tools.defaultBashTimeout 决定，此值只防御直接调用本包的场景。
	fallbackTimeout = 30 * time.Second
	// tempFilePattern 是命令输出临时文件的名字模式。
	tempFilePattern = "laxbash-*"
	// shellBin 是执行命令用的 shell。
	shellBin = "bash"
)

// Runner 是基于 os/exec 的 tools.ShellRunner 实现，与一次 agent 运行同生命周期。
type Runner struct {
	// procs 登记每次调用派生的进程与输出临时文件，供 Close 统一回收
	// LLM 遗忘清理的后台进程
	mu    sync.Mutex
	procs []procRecord
}

type procRecord struct {
	pid      int
	tempfile string
}

// New 返回一个 Runner；使用完毕须调用 Close 回收遗留后台进程与临时文件。
func New() *Runner { return &Runner{} }

// 编译期确保 Runner 满足领域端口。
var _ tools.ShellRunner = (*Runner)(nil)

// Run 在 workDir 下以 bash -c 执行 command，至多等待 timeout。
// 命令非零退出不算错误，退出码与原始错误描述填入 ShellOutcome；
// 仅超时/取消、无法启动、无法读取输出等基础设施失败才返回 error。
func (r *Runner) Run(ctx context.Context, workDir, command string, timeout time.Duration) (tools.ShellOutcome, error) {
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 输出落临时文件而非 CombinedOutput 的内部管道：后台子进程继承的
	// 管道写端会让 Wait 永久阻塞（超时也救不了）；文件句柄则随主命令
	// 退出即可返回，后台进程还能继续安全写入
	tmp, err := os.CreateTemp("", tempFilePattern)
	if err != nil {
		return tools.ShellOutcome{}, fmt.Errorf("创建命令输出临时文件失败: %w", err)
	}

	cmd := exec.CommandContext(ctx, shellBin, "-c", command)
	cmd.Dir = workDir
	cmd.Stdout = tmp
	cmd.Stderr = tmp
	// 独立进程组 + 覆盖 CommandContext 默认的"只杀直接子进程"；两者都必须在
	// Start 之前装配完成，具体理由见平台文件注释
	setProcGroup(cmd)
	installCancelKill(cmd)

	if err := cmd.Start(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return tools.ShellOutcome{}, fmt.Errorf("启动命令失败: %w", err)
	}

	// 登记须在 Wait 之前：命令超时后其后台派生进程仍需 Close 兜底回收
	r.track(cmd.Process.Pid, tmp.Name())
	waitErr := cmd.Wait()
	_ = tmp.Close()

	// 超时/取消：交回原始 ctx 错误，由领域层据 errors.Is 区分面向模型的文案
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tools.ShellOutcome{}, ctxErr
	}

	output, readErr := os.ReadFile(tmp.Name())
	if readErr != nil {
		return tools.ShellOutcome{}, fmt.Errorf("读取命令输出失败: %w", readErr)
	}

	outcome := tools.ShellOutcome{Output: string(output)}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			// 非退出码类失败（如 I/O 异常）属基础设施错误，不当作命令结果
			return tools.ShellOutcome{}, waitErr
		}
		outcome.ExitCode = exitErr.ExitCode()
		outcome.ExitErr = waitErr.Error()
	}
	return outcome, nil
}

// Close 回收本 Runner 生命周期内由命令启动的进程（含 LLM 遗忘清理的后台
// 进程）并删除输出临时文件，随会话结束由 Registry.Close 统一调用。
func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, p := range r.procs {
		// 进程已随命令正常退出时报 ESRCH（或平台等价错误），属预期，忽略
		_ = terminatePID(p.pid)
		if err := os.Remove(p.tempfile); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	r.procs = nil
	return errors.Join(errs...)
}

func (r *Runner) track(pid int, tempfile string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procs = append(r.procs, procRecord{pid: pid, tempfile: tempfile})
}

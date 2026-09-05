package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	laxctx "github.com/mikellxy/laxcode/internal/context"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	ToolBash           = "bash"
	ToolReadFile       = "read_file"
	ToolWriteFile      = "write_file"
	ToolEditFile       = "edit_file"
	ToolRunSubAgent    = "run_sub_agent"
	defaultBashTimeout = 30 * time.Second
)

type BashTool struct {
	WorkDir string
	// Timeout 是单条命令的超时上限，零值取默认 30s；测试可缩短
	Timeout time.Duration

	// procs 登记每次调用派生的进程组与输出临时文件，供 Close 统一
	// 回收 LLM 遗忘清理的后台进程
	mu    sync.Mutex
	procs []bashProcRecord
}

type bashProcRecord struct {
	pgid     int
	tempfile string
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{WorkDir: workDir, Timeout: defaultBashTimeout}
}

func (b *BashTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (b *BashTool) BeforeExecInfo(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return ToolBash + "()"
	}
	command, ok := argsMap["command"]
	if !ok {
		return ToolBash + "()"
	}

	return fmt.Sprintf("%s(%s)", ToolBash, command)
}

func (b *BashTool) Name() string {
	return ToolBash
}

func (b *BashTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name: b.Name(),
		Description: "在工作目录执行 bash 命令。需要后台进程（如启动服务器）时，" +
			"务必重定向输出到日志文件并记录pid，例如: " +
			"python3 server.py > /tmp/srv.log 2>&1 & echo \"pid=$!\"，" +
			"之后用返回的pid执行 kill -9 <pid> 清理，也可 tail 日志文件排错",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "执行bash 命令，如 grep -rn NewAgentEngine",
				},
			},
			"required": []string{"command"},
		},
	}
}

type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Truncated bool   `json:"is_truncated"`
	Desc      string `json:"desc"`
}

func (e *ExecResult) String() string {
	s := "%s\nexit_code:%d\nstdout_truncated:%v\nstdout:%s"
	return fmt.Sprintf(s, e.Desc, e.ExitCode, e.Truncated, e.Stdout)
}

func (b *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {

	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, err)
	}
	command, ok := argsMap["command"]
	if !ok || strings.TrimSpace(command) == "" {
		return "", laxctx.NewErrorWithPrompt(&laxctx.ParamError{}, errors.New("command required"))
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()

	// 输出落临时文件而非 CombinedOutput 的内部管道：后台子进程继承的
	// 管道写端会让 Wait 永久阻塞（超时也救不了）；文件句柄则随主命令
	// 退出即可返回，后台进程还能继续安全写入
	tmp, err := os.CreateTemp("", "laxbash-*")
	if err != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{}, err)
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = b.WorkDir
	cmd.Stdout = tmp
	cmd.Stderr = tmp
	// 独立进程组：超时时收割整棵进程树（含后台派生），且不波及其他
	// 命令留下的后台进程
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{}, err)
	}
	// 覆盖 CommandContext 默认的"只杀直接子进程"，改为按负 pid 杀整组
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	b.track(cmd.Process.Pid, tmp.Name())
	err = cmd.Wait()
	_ = tmp.Close()

	if ctx.Err() != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{},
			fmt.Errorf("bash执行超时或被取消: %w", ctx.Err()))
	}

	output, readErr := os.ReadFile(tmp.Name())
	if readErr != nil {
		return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{},
			fmt.Errorf("读取命令输出失败: %w", readErr))
	}

	result := &ExecResult{Desc: "命令执行成功", Stdout: string(output)}
	if err != nil {
		result.Desc = "命令执行失败: " + err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return "", laxctx.NewErrorWithPrompt(&laxctx.BashExecuteError{}, err)
		}
	}

	const maxRune = 8000
	result.Stdout, result.Truncated = safeTruncateUTF8(result.Stdout, maxRune)
	if result.Truncated {
		result.Desc += " ;bash输出过长已截断至前:" + strconv.Itoa(maxRune) + "字符"
	}

	return result.String(), nil
}

// Close 回收本次运行内由 bash 工具启动的进程组（含 LLM 遗忘清理的后台
// 进程）并删除输出临时文件，随会话结束由 Registry.Close 统一调用
func (b *BashTool) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var errs []error
	for _, p := range b.procs {
		// 组已随命令正常退出时返回 ESRCH，属预期，忽略
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		if err := os.Remove(p.tempfile); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	b.procs = nil
	return errors.Join(errs...)
}

func (b *BashTool) track(pgid int, tempfile string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.procs = append(b.procs, bashProcRecord{pgid: pgid, tempfile: tempfile})
}

func (b *BashTool) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return defaultBashTimeout
}

func safeTruncateUTF8(s string, maxRune int) (out string, truncated bool) {
	r := []rune(s)
	if len(r) <= maxRune {
		return s, false
	}
	return string(r[:maxRune]), true
}

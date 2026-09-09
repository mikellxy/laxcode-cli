//go:build unix

package shell

// Runner 的行为测试：全部依赖真实 bash 与 POSIX 进程组语义，故以 unix
// 构建标签隔离（本包在任意 GOOS 下均可编译，见 proc_other.go）。
// bash 工具的参数校验、结果文案与截断在 domain/tools/bash_test.go 用替身覆盖。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func runShell(t *testing.T, r *Runner, workDir, command string, timeout time.Duration) (tools.ShellOutcome, error) {
	t.Helper()
	return r.Run(context.Background(), workDir, command, timeout)
}

// parseBgPid 从输出中提取 "pid=NNN" 形式的后台进程 pid
func parseBgPid(t *testing.T, out string) int {
	t.Helper()
	idx := strings.Index(out, "pid=")
	if idx < 0 {
		t.Fatalf("output missing pid=: %q", out)
	}
	rest := out[idx+len("pid="):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("parse pid from %q: %v", out, err)
	}
	return pid
}

func procAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitProcDead 轮询等待进程退出：被杀后到被回收前存在僵尸窗口，
// kill(pid,0) 对僵尸进程仍返回成功
func waitProcDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for procAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after 2s", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func TestRunMergedOutput(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	outcome, err := runShell(t, r, t.TempDir(), "echo out; echo err 1>&2", 30*time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(outcome.Output, "out") || !strings.Contains(outcome.Output, "err") {
		t.Errorf("merged output missing streams: %q", outcome.Output)
	}
	if outcome.ExitCode != 0 || outcome.ExitErr != "" {
		t.Errorf("ExitCode=%d ExitErr=%q, want 0/empty", outcome.ExitCode, outcome.ExitErr)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	outcome, err := runShell(t, r, t.TempDir(), "echo boom; exit 3", 30*time.Second)
	if err != nil {
		t.Fatalf("非零退出不应返回 error，实际 = %v", err)
	}
	if outcome.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", outcome.ExitCode)
	}
	if outcome.ExitErr != "exit status 3" {
		t.Errorf("ExitErr = %q, want %q", outcome.ExitErr, "exit status 3")
	}
	if !strings.Contains(outcome.Output, "boom") {
		t.Errorf("output missing stdout: %q", outcome.Output)
	}
}

// TestRunHonorsWorkDir 验证命令在指定工作目录执行，而非进程当前目录。
func TestRunHonorsWorkDir(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	workDir := t.TempDir()
	// 用 pwd -P 取物理路径：macOS 的 TempDir 经 /var → /private/var 符号链接，
	// 而 bash 内建 pwd 缺省输出逻辑路径，两者不可直接比对
	outcome, err := runShell(t, r, workDir, "pwd -P", 30*time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got := strings.TrimSpace(outcome.Output); got != want {
		t.Errorf("pwd -P = %q, want %q", got, want)
	}
}

// TestRunBackgroundProcessReturnsImmediately 验证后台派生进程不阻塞 Run 返回，
// 且在 Run 返回后仍存活，直到 Close 兜底回收。
func TestRunBackgroundProcessReturnsImmediately(t *testing.T) {
	r := New()
	start := time.Now()
	outcome, err := runShell(t, r, t.TempDir(), "sleep 30 & echo pid=$!", 30*time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run() blocked %v behind background process", elapsed)
	}
	pid := parseBgPid(t, outcome.Output)
	if !procAlive(pid) {
		t.Errorf("background process %d should survive after Run returns", pid)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	waitProcDead(t, pid)
}

func TestCloseRemovesTempFiles(t *testing.T) {
	r := New()
	glob := filepath.Join(os.TempDir(), tempFilePattern)
	before, _ := filepath.Glob(glob)

	if _, err := runShell(t, r, t.TempDir(), "echo hi", 30*time.Second); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	after, _ := filepath.Glob(glob)
	if len(after) != len(before)+1 {
		t.Fatalf("expected one temp file created, before=%d after=%d", len(before), len(after))
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	final, _ := filepath.Glob(glob)
	if len(final) != len(before) {
		t.Errorf("temp files not removed by Close(), before=%d final=%d", len(before), len(final))
	}
}

// TestCloseIsIdempotent 验证 Close 可重复调用：Registry.Close 与信号处理路径
// 可能各自触发一次。
func TestCloseIsIdempotent(t *testing.T) {
	r := New()
	if _, err := runShell(t, r, t.TempDir(), "echo hi", 30*time.Second); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close() error = %v", err)
	}
}

func TestRunTimeoutKillsProcess(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	start := time.Now()
	_, err := runShell(t, r, t.TempDir(), "sleep 30", 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// 端口契约：超时须可被 errors.Is 识别，领域层据此区分文案
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v, group kill did not unblock Wait", elapsed)
	}
}

// TestRunParentContextCanceled 验证父 ctx 取消同样交回可识别的原因。
func TestRunParentContextCanceled(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := r.Run(ctx, t.TempDir(), "sleep 30", 30*time.Second)
	if err == nil {
		t.Fatal("expected canceled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
}

func TestRunTimeoutKillsBackgroundChildren(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	marker := "laxshell-test-marker-9871"
	_, err := runShell(t, r, t.TempDir(), fmt.Sprintf("sleep 30 %s & sleep 30", marker), 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// 组杀必须连后台派生进程一起收割
	deadline := time.Now().Add(2 * time.Second)
	for {
		pgrep := exec.Command("pgrep", "-f", marker)
		if pgrep.Run() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("background process with marker %q still alive after timeout", marker)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunTimeoutDoesNotKillEarlierBackground(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	outcome, err := runShell(t, r, t.TempDir(), "sleep 30 & echo pid=$!", 30*time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	pid := parseBgPid(t, outcome.Output)

	if _, err := runShell(t, r, t.TempDir(), "sleep 30", 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout error")
	}
	if !procAlive(pid) {
		t.Errorf("earlier background process %d was killed by an unrelated timeout", pid)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// TestRunFallbackTimeout 验证 timeout<=0 时回落到兜底上限而非立即超时。
func TestRunFallbackTimeout(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })

	start := time.Now()
	outcome, err := runShell(t, r, t.TempDir(), "sleep 0.2; echo done", 0)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(outcome.Output, "done") {
		t.Errorf("output = %q, want it to contain %q", outcome.Output, "done")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("命令未真正等待即返回，elapsed = %v", elapsed)
	}
}

func TestRunBackgroundServerIntegration(t *testing.T) {
	// 集成测试：真实后台服务器 + curl 场景，验证"启动-测试-清理"完整
	// 工作流；默认跳过，LAXCODE_INTEGRATION=1 显式开启
	if os.Getenv("LAXCODE_INTEGRATION") == "" {
		t.Skip("skipping integration test; set LAXCODE_INTEGRATION=1 to run")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	port := freeTCPPort(t)

	r := New()
	t.Cleanup(func() { _ = r.Close() })

	start := time.Now()
	outcome, err := runShell(t, r, t.TempDir(), fmt.Sprintf(
		`%s -m http.server %d --bind 127.0.0.1 > /tmp/laxshell-srv.log 2>&1 & echo "pid=$!"; sleep 0.5; curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/`,
		python, port, port), 30*time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run() blocked %v behind background server", elapsed)
	}
	if !strings.Contains(outcome.Output, "200") {
		t.Errorf("curl did not get 200, output: %q", outcome.Output)
	}
	pid := parseBgPid(t, outcome.Output)
	if !procAlive(pid) {
		t.Fatal("server should survive between calls")
	}

	if _, err := runShell(t, r, t.TempDir(), fmt.Sprintf("kill -9 %d", pid), 30*time.Second); err != nil {
		t.Fatalf("kill Run() error = %v", err)
	}
	waitProcDead(t, pid)
}

package tools

import (
	"context"
	"encoding/json"
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
)

func execBash(t *testing.T, b *BashTool, command string) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b.Execute(context.Background(), args)
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

func newTestBashTool(t *testing.T) *BashTool {
	t.Helper()
	return NewBashTool(t.TempDir())
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

func TestBashToolMergedOutput(t *testing.T) {
	b := newTestBashTool(t)
	out, err := execBash(t, b, "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("merged output missing streams: %q", out)
	}
	if !strings.Contains(out, "exit_code:0") {
		t.Errorf("expected exit_code:0 in: %q", out)
	}
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolNonZeroExit(t *testing.T) {
	b := newTestBashTool(t)
	out, err := execBash(t, b, "echo boom; exit 3")
	if err != nil {
		t.Fatalf("Execute() should not return error for non-zero exit: %v", err)
	}
	if !strings.Contains(out, "exit_code:3") {
		t.Errorf("expected exit_code:3 in: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("output missing stdout: %q", out)
	}
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolTruncateOutput(t *testing.T) {
	b := newTestBashTool(t)
	out, err := execBash(t, b, "head -c 10000 /dev/zero | tr '\\0' 'a'")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "stdout_truncated:true") {
		t.Errorf("expected truncated flag in: %q", out)
	}
	idx := strings.Index(out, "stdout:")
	if idx < 0 {
		t.Fatalf("missing stdout section: %q", out)
	}
	if got := len([]rune(out[idx+len("stdout:"):])); got != 8000 {
		t.Errorf("truncated stdout rune count = %d, want 8000", got)
	}
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolBackgroundProcessReturnsImmediately(t *testing.T) {
	b := newTestBashTool(t)
	start := time.Now()
	out, err := execBash(t, b, "sleep 30 & echo pid=$!")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Execute() blocked %v behind background process", elapsed)
	}
	pid := parseBgPid(t, out)
	if !procAlive(pid) {
		t.Errorf("background process %d should survive after Execute returns", pid)
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	waitProcDead(t, pid)
}

func TestBashToolCloseRemovesTempFiles(t *testing.T) {
	b := newTestBashTool(t)
	glob := filepath.Join(os.TempDir(), "laxbash-*")
	before, _ := filepath.Glob(glob)

	if _, err := execBash(t, b, "echo hi"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	after, _ := filepath.Glob(glob)
	if len(after) != len(before)+1 {
		t.Fatalf("expected one temp file created, before=%d after=%d", len(before), len(after))
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	final, _ := filepath.Glob(glob)
	if len(final) != len(before) {
		t.Errorf("temp files not removed by Close(), before=%d final=%d", len(before), len(final))
	}
}

func TestBashToolTimeoutKillsProcess(t *testing.T) {
	b := newTestBashTool(t)
	b.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := execBash(t, b, "sleep 30")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v, group kill did not unblock Wait", elapsed)
	}
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolTimeoutKillsBackgroundChildren(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	b := newTestBashTool(t)
	b.Timeout = 300 * time.Millisecond
	marker := "laxbash-test-marker-9871"
	_, err := execBash(t, b, fmt.Sprintf("sleep 30 %s & sleep 30", marker))
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
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolTimeoutDoesNotKillEarlierBackground(t *testing.T) {
	b := newTestBashTool(t)
	out, err := execBash(t, b, "sleep 30 & echo pid=$!")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	pid := parseBgPid(t, out)

	b.Timeout = 300 * time.Millisecond
	if _, err := execBash(t, b, "sleep 30"); err == nil {
		t.Fatal("expected timeout error")
	}
	if !procAlive(pid) {
		t.Errorf("earlier background process %d was killed by an unrelated timeout", pid)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	t.Cleanup(func() { _ = b.Close() })
}

func TestBashToolBackgroundServerIntegration(t *testing.T) {
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

	b := newTestBashTool(t)
	start := time.Now()
	out, err := execBash(t, b, fmt.Sprintf(
		`%s -m http.server %d --bind 127.0.0.1 > /tmp/laxbash-srv.log 2>&1 & echo "pid=$!"; sleep 0.5; curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/`,
		python, port, port))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Execute() blocked %v behind background server", elapsed)
	}
	if !strings.Contains(out, "200") {
		t.Errorf("curl did not get 200, output: %q", out)
	}
	pid := parseBgPid(t, out)
	if !procAlive(pid) {
		t.Fatal("server should survive between tool calls")
	}

	if _, err := execBash(t, b, fmt.Sprintf("kill -9 %d", pid)); err != nil {
		t.Fatalf("kill Execute() error = %v", err)
	}
	waitProcDead(t, pid)
	t.Cleanup(func() { _ = b.Close() })
}

package tools

// bash 工具的领域测试：用 ShellRunner 替身验证参数校验、结果文案编排、
// 输出截断、超时/取消的错误分类，以及工作目录与超时的正确透传。
// 真实进程行为（后台进程存活、进程组收割、临时文件回收）在
// infrastructure/shell 的测试中覆盖。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner 是 ShellRunner 的测试替身：记录调用入参并回放预设结果。
type fakeRunner struct {
	outcome  ShellOutcome
	err      error
	closeErr error

	closed     int
	gotWorkDir string
	gotCommand string
	gotTimeout time.Duration
}

func (f *fakeRunner) Run(_ context.Context, workDir, command string, timeout time.Duration) (ShellOutcome, error) {
	f.gotWorkDir = workDir
	f.gotCommand = command
	f.gotTimeout = timeout
	return f.outcome, f.err
}

func (f *fakeRunner) Close() error {
	f.closed++
	return f.closeErr
}

// 编译期确保替身满足端口。
var _ ShellRunner = (*fakeRunner)(nil)

func newTestBashTool(t *testing.T, runner ShellRunner) *BashTool {
	t.Helper()
	return NewBashTool(t.TempDir(), runner)
}

func execBash(t *testing.T, b *BashTool, command string) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b.Execute(context.Background(), args)
}

// asPromptErr 断言 err 携带面向模型的自愈提示词。
func asPromptErr(t *testing.T, err error) string {
	t.Helper()
	var promptErr ErrorWithPrompt
	if !errors.As(err, &promptErr) {
		t.Fatalf("err 应实现 ErrorWithPrompt，实际 %T: %v", err, err)
	}
	prompt, ok := promptErr.AsPrompt()
	if !ok || prompt == "" {
		t.Fatalf("AsPrompt 应返回非空提示词，实际 (%q, %v)", prompt, ok)
	}
	return prompt
}

func TestBashToolExecuteSuccess(t *testing.T) {
	runner := &fakeRunner{outcome: ShellOutcome{Output: "hello\n", ExitCode: 0}}
	b := newTestBashTool(t, runner)

	out, err := execBash(t, b, "echo hello")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "命令执行成功") {
		t.Errorf("缺成功描述: %q", out)
	}
	if !strings.Contains(out, "exit_code:0") {
		t.Errorf("expected exit_code:0 in: %q", out)
	}
	if !strings.Contains(out, "stdout_truncated:false") {
		t.Errorf("expected stdout_truncated:false in: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("缺命令输出: %q", out)
	}
}

// TestBashToolNonZeroExitIsNotError 锁定端口契约：非零退出由 ShellOutcome
// 承载而非 error，工具须把退出码与原始错误描述一并回给模型。
func TestBashToolNonZeroExitIsNotError(t *testing.T) {
	runner := &fakeRunner{outcome: ShellOutcome{
		Output: "boom\n", ExitCode: 3, ExitErr: "exit status 3",
	}}
	b := newTestBashTool(t, runner)

	out, err := execBash(t, b, "echo boom; exit 3")
	if err != nil {
		t.Fatalf("非零退出不应返回 error，实际 = %v", err)
	}
	if !strings.Contains(out, "exit_code:3") {
		t.Errorf("expected exit_code:3 in: %q", out)
	}
	if !strings.Contains(out, "命令执行失败: exit status 3") {
		t.Errorf("缺失败描述与原始错误: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("失败时仍应带上原始输出: %q", out)
	}
}

func TestBashToolTruncateOutput(t *testing.T) {
	runner := &fakeRunner{outcome: ShellOutcome{Output: strings.Repeat("a", 10000)}}
	b := newTestBashTool(t, runner)

	out, err := execBash(t, b, "head -c 10000 /dev/zero | tr '\\0' 'a'")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "stdout_truncated:true") {
		t.Errorf("expected truncated flag in: %q", out)
	}
	if !strings.Contains(out, "bash输出过长已截断至前:8000字符") {
		t.Errorf("缺截断说明: %q", out)
	}
	idx := strings.Index(out, "stdout:")
	if idx < 0 {
		t.Fatalf("missing stdout section: %q", out)
	}
	// ExecResult.String 不追加尾换行，stdout: 之后即截断后的正文
	if got := len([]rune(out[idx+len("stdout:"):])); got != 8000 {
		t.Errorf("truncated stdout rune count = %d, want 8000", got)
	}
}

// TestBashToolTruncateKeepsMultibyteRunes 验证按 rune 而非字节截断，
// 不会把多字节字符切成半个。
func TestBashToolTruncateKeepsMultibyteRunes(t *testing.T) {
	runner := &fakeRunner{outcome: ShellOutcome{Output: strings.Repeat("中", 9000)}}
	b := newTestBashTool(t, runner)

	out, err := execBash(t, b, "cmd")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	idx := strings.Index(out, "stdout:")
	if idx < 0 {
		t.Fatalf("missing stdout section: %q", out)
	}
	body := out[idx+len("stdout:"):]
	if got := len([]rune(body)); got != 8000 {
		t.Errorf("truncated rune count = %d, want 8000", got)
	}
	if strings.Contains(body, "\uFFFD") {
		t.Error("截断不应产生半字符（U+FFFD）")
	}
}

// TestBashToolTimeoutWording 验证超时被归为 BashExecuteError 且文案引导模型
// 改用后台进程，而非误判为命令写错。
func TestBashToolTimeoutWording(t *testing.T) {
	runner := &fakeRunner{err: context.DeadlineExceeded}
	b := newTestBashTool(t, runner)

	_, err := execBash(t, b, "sleep 30")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "bash执行超时或被取消") {
		t.Errorf("超时文案不符: %v", err)
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("超时应保留底层原因: %v", err)
	}
	asPromptErr(t, err)
}

func TestBashToolCanceledWording(t *testing.T) {
	runner := &fakeRunner{err: context.Canceled}
	b := newTestBashTool(t, runner)

	_, err := execBash(t, b, "sleep 30")
	if err == nil {
		t.Fatal("expected canceled error")
	}
	if !strings.Contains(err.Error(), "bash执行超时或被取消") {
		t.Errorf("取消应复用超时文案: %v", err)
	}
}

func TestBashToolRunnerInfraError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("启动命令失败: fork/exec: resource exhausted")}
	b := newTestBashTool(t, runner)

	_, err := execBash(t, b, "echo hi")
	if err == nil {
		t.Fatal("expected infra error")
	}
	if strings.Contains(err.Error(), "bash执行超时或被取消") {
		t.Errorf("基础设施失败不应套用超时文案: %v", err)
	}
	asPromptErr(t, err)
}

// TestBashToolPassesWorkDirAndTimeout 验证工作目录与超时上限被正确透传给端口，
// 且零值 Timeout 回落到默认 30s。
func TestBashToolPassesWorkDirAndTimeout(t *testing.T) {
	runner := &fakeRunner{outcome: ShellOutcome{Output: "ok"}}
	b := newTestBashTool(t, runner)

	if _, err := execBash(t, b, "echo ok"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.gotWorkDir != b.WorkDir {
		t.Errorf("workDir = %q, want %q", runner.gotWorkDir, b.WorkDir)
	}
	if runner.gotCommand != "echo ok" {
		t.Errorf("command = %q, want %q", runner.gotCommand, "echo ok")
	}
	if runner.gotTimeout != defaultBashTimeout {
		t.Errorf("timeout = %v, want default %v", runner.gotTimeout, defaultBashTimeout)
	}

	b.Timeout = 300 * time.Millisecond
	if _, err := execBash(t, b, "echo ok"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.gotTimeout != 300*time.Millisecond {
		t.Errorf("timeout = %v, want 300ms", runner.gotTimeout)
	}
}

// TestBashToolCloseDelegates 验证 Close 透传到端口：后台进程与临时文件的回收
// 责任在基础设施侧，工具只负责在会话结束时触发。
func TestBashToolCloseDelegates(t *testing.T) {
	runner := &fakeRunner{}
	b := newTestBashTool(t, runner)

	if err := b.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if runner.closed != 1 {
		t.Errorf("Runner.Close 调用次数 = %d, want 1", runner.closed)
	}

	wantErr := errors.New("remove tempfile: permission denied")
	runner.closeErr = wantErr
	if err := b.Close(); !errors.Is(err, wantErr) {
		t.Errorf("Close() = %v, want %v", err, wantErr)
	}
}

func TestBashToolParamValidation(t *testing.T) {
	b := newTestBashTool(t, &fakeRunner{})
	ctx := context.Background()

	t.Run("非法 JSON", func(t *testing.T) {
		if _, err := b.Execute(ctx, json.RawMessage(`{bad json`)); err == nil {
			t.Fatal("expected param error")
		} else {
			asPromptErr(t, err)
		}
	})

	t.Run("缺 command", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{})
		if _, err := b.Execute(ctx, args); err == nil {
			t.Fatal("expected param error")
		} else if !strings.Contains(err.Error(), "command required") {
			t.Errorf("err = %v, want it to mention %q", err, "command required")
		}
	})

	t.Run("command 为空白", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "   "})
		if _, err := b.Execute(ctx, args); err == nil {
			t.Fatal("expected param error")
		}
	})
}

func TestBashToolExecInfo(t *testing.T) {
	b := newTestBashTool(t, &fakeRunner{})

	if got := b.Name(); got != ToolBash {
		t.Errorf("Name = %q, want %q", got, ToolBash)
	}
	if got := b.AfterExecInfo(nil); got != "" {
		t.Errorf("AfterExecInfo = %q, want empty", got)
	}

	args, _ := json.Marshal(map[string]string{"command": "grep -rn Foo"})
	if got := b.BeforeExecInfo(args); got != "bash(grep -rn Foo)" {
		t.Errorf("BeforeExecInfo = %q, want %q", got, "bash(grep -rn Foo)")
	}
	if got := b.BeforeExecInfo(json.RawMessage(`{bad`)); got != "bash()" {
		t.Errorf("非法入参应回落占位文案，实际 %q", got)
	}
	if got := b.BeforeExecInfo(json.RawMessage(`{}`)); got != "bash()" {
		t.Errorf("缺 command 应回落占位文案，实际 %q", got)
	}

	def := b.Definition()
	if def.Name != ToolBash {
		t.Errorf("Definition.Name = %q, want %q", def.Name, ToolBash)
	}
	if def.Description == "" {
		t.Error("Definition.Description 不应为空")
	}
	params, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Definition.Parameters 应含 properties：%v", def.Parameters)
	}
	if _, ok := params["command"]; !ok {
		t.Errorf("command 属性缺失：%v", params)
	}
}

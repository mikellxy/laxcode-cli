package cliprinter

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// newTestModel 构造一个带缓冲通道的 model，便于测试中直接调用 Cmd 而不阻塞。
func newTestModel() (*model, chan string, chan string) {
	out := make(chan string, 8)
	in := make(chan string, 8)
	m := &model{lines: []string{""}, outChan: out, inChan: in}
	return m, out, in
}

func keyEnter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func keyCtrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }
func keyText(s string) tea.KeyPressMsg {
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func TestStreamEndIsControlChar(t *testing.T) {
	if StreamEnd != "\x04" {
		t.Errorf("StreamEnd = %q，期望 EOT \\x04", StreamEnd)
	}
}

func TestInsertMovesCursor(t *testing.T) {
	m, _, _ := newTestModel()
	m.insert("hello")
	if m.lines[0] != "hello" || m.col != 5 {
		t.Fatalf("insert 后 lines=%q col=%d", m.lines[0], m.col)
	}
	m.col = 2
	m.insert("XY")
	if m.lines[0] != "heXYllo" || m.col != 4 {
		t.Fatalf("中间插入后 lines=%q col=%d", m.lines[0], m.col)
	}
}

func TestDeleteBackspaceMergesLines(t *testing.T) {
	m, _, _ := newTestModel()
	m.insert("abcd")
	m.deleteBackspace()
	if m.lines[0] != "abc" || m.col != 3 {
		t.Fatalf("行尾退格后 lines=%q col=%d", m.lines[0], m.col)
	}
	m.newline() // 拆成 "abc" / ""，row=1 col=0
	if m.row != 1 {
		t.Fatalf("newline 后 row=%d", m.row)
	}
	m.deleteBackspace() // 第二行行首退格 → 与上一行合并
	if len(m.lines) != 1 || m.lines[0] != "abc" || m.row != 0 || m.col != 3 {
		t.Fatalf("合并行后 lines=%v row=%d col=%d", m.lines, m.row, m.col)
	}
}

func TestNewlineSplitsAtCursor(t *testing.T) {
	m, _, _ := newTestModel()
	m.insert("abcd")
	m.col = 2
	m.newline()
	if len(m.lines) != 2 || m.lines[0] != "ab" || m.lines[1] != "cd" || m.row != 1 || m.col != 0 {
		t.Fatalf("newline 后 lines=%v row=%d col=%d", m.lines, m.row, m.col)
	}
}

func TestTermWidthAndHline(t *testing.T) {
	m, _, _ := newTestModel()
	if m.termWidth() != 80 {
		t.Errorf("未知宽度应回退 80，got %d", m.termWidth())
	}
	m.width = 42
	if m.termWidth() != 42 {
		t.Errorf("应返回实际宽度 42，got %d", m.termWidth())
	}
	if got := len([]rune(m.hline())); got != 42 {
		t.Errorf("hline 宽度=%d，期望 42", got)
	}
}

func TestUserMessageViewPadsToWidth(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 20
	// "你好" 显示宽度 4，应补空格至 20；ansi.StringWidth 会忽略 ANSI 转义
	view := m.userMessageView("你好")
	line := strings.TrimSuffix(view, "\n")
	if w := ansi.StringWidth(line); w != 20 {
		t.Errorf("补齐后显示宽度=%d，期望 20（line=%q）", w, line)
	}
	if !strings.Contains(view, "\x1b[48;5;250m") {
		t.Errorf("用户消息应含浅灰背景转义，got %q", view)
	}
}

func TestInputViewPromptFixedOnFirstLine(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"first", "second"}
	m.row, m.col = 1, 0 // 光标在第二行
	lines := strings.Split(m.inputView(), "\n")
	if len(lines) != 2 {
		t.Fatalf("inputView 行数=%d，期望 2", len(lines))
	}
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("第一行应以 '> ' 开头（不跟随光标行），got %q", lines[0])
	}
	if strings.HasPrefix(lines[1], ">") {
		t.Errorf("光标所在行不应出现 '>'，got %q", lines[1])
	}
}

func TestUpdateRecordsWidth(t *testing.T) {
	m, _, _ := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.width != 100 {
		t.Errorf("width=%d，期望 100", m.width)
	}
}

func TestUpdateEnterSendsToOutChan(t *testing.T) {
	m, out, _ := newTestModel()
	m.insert("hi")
	_, cmd := m.Update(keyEnter())

	if m.phase != phaseSending {
		t.Errorf("enter 后应进入 phaseSending，got %v", m.phase)
	}
	if len(m.messages) != 1 || m.messages[0].text != "hi" || !m.messages[0].fromUser {
		t.Errorf("应追加用户消息 hi，got %+v", m.messages)
	}
	if len(m.lines) != 1 || m.lines[0] != "" || m.row != 0 || m.col != 0 {
		t.Errorf("输入区应清空，got lines=%v row=%d col=%d", m.lines, m.row, m.col)
	}
	if cmd == nil {
		t.Fatal("enter 应返回 sendOut 命令")
	}
	// 调用命令：应把输入写入 outChan（缓冲，不阻塞）并返回 outFlushedMsg
	msg := cmd()
	if _, ok := msg.(outFlushedMsg); !ok {
		t.Errorf("sendOut 命令应返回 outFlushedMsg，got %T", msg)
	}
	if got := <-out; got != "hi" {
		t.Errorf("outChan 收到 %q，期望 hi", got)
	}
}

func TestUpdateEnterSkipsEmptyInput(t *testing.T) {
	m, out, _ := newTestModel()
	m.insert("   ")
	_, cmd := m.Update(keyEnter())

	if m.phase != phaseInput {
		t.Errorf("空输入应保持 phaseInput，got %v", m.phase)
	}
	if len(m.messages) != 0 {
		t.Errorf("空输入不应追加消息，got %+v", m.messages)
	}
	if cmd != nil {
		t.Errorf("空输入不应返回命令")
	}
	select {
	case v := <-out:
		t.Errorf("空输入不应写入 outChan，got %q", v)
	default:
	}
}

func TestUpdateOutFlushedStartsStreaming(t *testing.T) {
	m, _, _ := newTestModel()
	_, cmd := m.Update(outFlushedMsg{})
	if m.phase != phaseStreaming {
		t.Errorf("outFlushed 后应进入 phaseStreaming，got %v", m.phase)
	}
	if len(m.messages) != 1 || m.messages[0].text != "" || m.messages[0].fromUser {
		t.Errorf("应追加一条空的对端消息，got %+v", m.messages)
	}
	if cmd == nil {
		t.Error("应返回 readIn 命令开始流式读取")
	}
}

func TestUpdateInChunkAppendsAndContinues(t *testing.T) {
	m, _, _ := newTestModel()
	m.messages = append(m.messages, message{}) // 模拟已开始流式的对端消息
	_, cmd := m.Update(inChunkMsg{text: "hello"})
	if cmd == nil {
		t.Error("收到普通 chunk 应继续 readIn")
	}
	m.Update(inChunkMsg{text: " world"})
	if got := m.messages[len(m.messages)-1].text; got != "hello world" {
		t.Errorf("chunk 应不断拼接，got %q", got)
	}
}

func TestUpdateInChunkStreamEndReturnsToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	m.messages = append(m.messages, message{text: "done\n"})
	_, cmd := m.Update(inChunkMsg{text: StreamEnd})
	if m.phase != phaseInput {
		t.Errorf("收到终止符应回到 phaseInput，got %v", m.phase)
	}
	if cmd != nil {
		t.Error("终止符应返回 nil 命令（本轮结束）")
	}
	if got := m.messages[len(m.messages)-1].text; got != "done\n" {
		t.Errorf("终止符不应被拼进内容，got %q", got)
	}
}

func TestUpdateInClosedReturnsToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	m.Update(inClosedMsg{})
	if m.phase != phaseInput {
		t.Errorf("inChan 关闭应回到 phaseInput，got %v", m.phase)
	}
}

func TestUpdateCtrlCQuits(t *testing.T) {
	m, _, _ := newTestModel()
	_, cmd := m.Update(keyCtrlC())
	if cmd == nil {
		t.Fatal("ctrl+c 应返回 tea.Quit 命令")
	}
}

func TestUpdateIgnoresEditingWhileNotInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	m.lines = []string{"draft"}
	_, cmd := m.Update(keyText("x"))
	if m.lines[0] != "draft" {
		t.Errorf("非输入阶段应忽略编辑，got %q", m.lines[0])
	}
	if cmd != nil {
		t.Error("非输入阶段应返回 nil 命令")
	}
}

func TestViewRendersHistoryAndSeparators(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 30
	m.messages = []message{
		{text: "q1", fromUser: true},
		{text: "[LaxCode] thinking: ...\n"},
	}
	content := m.View().Content

	if !strings.Contains(content, "\x1b[48;5;250m") {
		t.Error("View 应把用户输入渲染为浅灰背景")
	}
	if !strings.Contains(content, "[LaxCode] thinking: ...") {
		t.Error("View 应包含对端回复内容")
	}
	// 输入区上下各一条与屏幕等宽的实线分隔符
	if n := strings.Count(content, strings.Repeat("─", 30)); n != 2 {
		t.Errorf("应有两条等宽实线分隔符，got %d", n)
	}
	if !strings.Contains(content, "> ") {
		t.Error("View 应包含固定在第一行的提示符 '> '")
	}
}

func TestViewSkipsEmptyAssistantMessage(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 10
	m.messages = []message{{text: "", fromUser: false}}
	content := m.View().Content
	// 空的对端消息不应产生多余空行：分隔符前不应有两个连续换行
	if strings.Contains(content, "\n\n"+strings.Repeat("─", 10)) {
		t.Errorf("空对端消息不应渲染出空行，got %q", content)
	}
}

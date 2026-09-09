package cliprinter

import (
	"reflect"
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

func TestUpdateRecordsSize(t *testing.T) {
	m, _, _ := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.width != 100 || m.height != 30 {
		t.Errorf("width=%d height=%d，期望 100/30", m.width, m.height)
	}
}

// TestSendOutWritesInputAndReturnsFlushed 单独验证发送命令：把输入写入 OutChan
// （缓冲，不阻塞）并返回 outFlushedMsg。enter 路径正是用它与回显组成 Sequence。
func TestSendOutWritesInputAndReturnsFlushed(t *testing.T) {
	out := make(chan string, 1)
	msg := sendOut(out, "hi")()
	if _, ok := msg.(outFlushedMsg); !ok {
		t.Errorf("sendOut 应返回 outFlushedMsg，got %T", msg)
	}
	if got := <-out; got != "hi" {
		t.Errorf("outChan 收到 %q，期望 hi", got)
	}
}

// TestReadInReturnsChunkThenClosed 验证读取命令：有内容返回 inChunkMsg，通道关闭返回 inClosedMsg。
func TestReadInReturnsChunkThenClosed(t *testing.T) {
	in := make(chan string, 1)
	in <- "x"
	close(in)
	if cm, ok := readIn(in)().(inChunkMsg); !ok || cm.text != "x" {
		t.Errorf("readIn 应先返回 inChunkMsg{x}，got %#v", cm)
	}
	if _, ok := readIn(in)().(inClosedMsg); !ok {
		t.Error("通道关闭后 readIn 应返回 inClosedMsg")
	}
}

func TestUpdateEnterClearsInputAndStartsSending(t *testing.T) {
	m, _, _ := newTestModel()
	m.insert("hi")
	_, cmd := m.Update(keyEnter())

	if m.phase != phaseSending {
		t.Errorf("enter 后应进入 phaseSending，got %v", m.phase)
	}
	if len(m.lines) != 1 || m.lines[0] != "" || m.row != 0 || m.col != 0 {
		t.Errorf("输入区应清空，got lines=%v row=%d col=%d", m.lines, m.row, m.col)
	}
	// 返回的是 Sequence(回显, 发送)：非 nil；发送本身由 TestSendOutWritesInputAndReturnsFlushed 覆盖
	if cmd == nil {
		t.Error("enter 应返回回显用户消息并发送到 OutChan 的命令")
	}
}

func TestUpdateEnterSkipsEmptyInput(t *testing.T) {
	m, out, _ := newTestModel()
	m.insert("   ")
	_, cmd := m.Update(keyEnter())

	if m.phase != phaseInput {
		t.Errorf("空输入应保持 phaseInput，got %v", m.phase)
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
	if cmd == nil {
		t.Error("应返回 readIn 命令开始流式读取")
	}
}

// TestAppendStreamSplitsCompleteLines 是本次修复的核心：完整行被切出交给打印命令
// （随后滚入 scrollback，可上翻），未结束的尾部留在 streamBuf。
func TestAppendStreamSplitsCompleteLines(t *testing.T) {
	m, _, _ := newTestModel()
	cmds := m.appendStream("l1\nl2\npartial")
	if m.streamBuf != "partial" {
		t.Errorf("尾部半行应留在 streamBuf，got %q", m.streamBuf)
	}
	if len(cmds) != 2 {
		t.Errorf("应切出两个完整行 → 2 个打印命令，got %d", len(cmds))
	}
}

func TestAppendStreamNoNewlineKeepsBuffer(t *testing.T) {
	m, _, _ := newTestModel()
	cmds := m.appendStream("partial")
	if m.streamBuf != "partial" {
		t.Errorf("无换行应全部留在 streamBuf，got %q", m.streamBuf)
	}
	if len(cmds) != 0 {
		t.Errorf("无完整行不应产生打印命令，got %d", len(cmds))
	}
}

func TestAppendStreamSkipsEmptyLines(t *testing.T) {
	m, _, _ := newTestModel()
	cmds := m.appendStream("a\n\nb\n")
	if len(cmds) != 2 {
		t.Errorf("空行应跳过，期望 2 个打印命令，got %d", len(cmds))
	}
	if m.streamBuf != "" {
		t.Errorf("全部内容以 \\n 结束，streamBuf 应清空，got %q", m.streamBuf)
	}
}

func TestFlushStreamClearsBuffer(t *testing.T) {
	m, _, _ := newTestModel()
	m.streamBuf = "tail"
	if cmd := m.flushStream(); cmd == nil {
		t.Error("有残留半行时 flushStream 应返回打印命令")
	}
	if m.streamBuf != "" {
		t.Errorf("flush 后 streamBuf 应清空，got %q", m.streamBuf)
	}
	if cmd := m.flushStream(); cmd != nil {
		t.Error("无残留时 flushStream 应返回 nil")
	}
}

func TestUpdateInChunkBuffersPartialLine(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	_, cmd := m.Update(inChunkMsg{text: "hel"})
	if m.streamBuf != "hel" {
		t.Errorf("未遇换行的 chunk 应留在 streamBuf，got %q", m.streamBuf)
	}
	if cmd == nil {
		t.Error("收到 chunk 应返回继续读取的命令")
	}
}

func TestUpdateInChunkStreamEndFlushesAndReturnsToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	m.streamBuf = "partial"
	_, cmd := m.Update(inChunkMsg{text: StreamEnd})
	if m.phase != phaseInput {
		t.Errorf("收到终止符应回到 phaseInput，got %v", m.phase)
	}
	if m.streamBuf != "" {
		t.Errorf("终止符应 flush 残留半行并清空 streamBuf，got %q", m.streamBuf)
	}
	if cmd == nil {
		t.Error("有残留半行时应返回打印命令")
	}
}

func TestUpdateStreamEndWithoutBufferReturnsNilCmd(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	_, cmd := m.Update(inChunkMsg{text: StreamEnd})
	if m.phase != phaseInput {
		t.Errorf("收到终止符应回到 phaseInput，got %v", m.phase)
	}
	if cmd != nil {
		t.Error("无残留半行时终止符应返回 nil 命令")
	}
}

func TestUpdateInClosedFlushesAndReturnsToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.phase = phaseStreaming
	m.streamBuf = "tail"
	m.Update(inClosedMsg{})
	if m.phase != phaseInput {
		t.Errorf("inChan 关闭应回到 phaseInput，got %v", m.phase)
	}
	if m.streamBuf != "" {
		t.Errorf("inChan 关闭应 flush 残留半行，got %q", m.streamBuf)
	}
}

func TestUpdateCtrlCQuits(t *testing.T) {
	m, _, _ := newTestModel()
	_, cmd := m.Update(keyCtrlC())
	if cmd == nil {
		t.Fatal("ctrl+c 应返回 tea.Quit 命令")
	}
}

func TestUpdatePasteAtCursor(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
		row, col   int
	}{
		{"unicode", "你好🙂", []string{"ab你好🙂cd", "tail"}, 0, 5},
		{"multiline", "甲\r\n\r乙\n", []string{"ab甲", "", "乙", "cd", "tail"}, 3, 0},
		{"controls", "x\x03\x04\x1by\t", []string{"abxy\tcd", "tail"}, 0, 5},
		{"empty", "", []string{"abcd", "tail"}, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, out, _ := newTestModel()
			m.lines, m.col = []string{"abcd", "tail"}, 2
			_, cmd := m.Update(tea.PasteMsg{Content: tc.text})
			if !reflect.DeepEqual(m.lines, tc.want) || m.row != tc.row || m.col != tc.col {
				t.Fatalf("paste: lines=%q cursor=(%d,%d), want %q (%d,%d)", m.lines, m.row, m.col, tc.want, tc.row, tc.col)
			}
			if cmd != nil || m.phase != phaseInput || len(out) != 0 {
				t.Fatal("粘贴（包括换行和 Ctrl+C）不应发送输入或退出")
			}
			m.View() // 粘贴后光标仍应能安全渲染和继续编辑。
			m.Update(keyText("!"))
		})
	}
}

func TestUpdateCommandCopyPaste(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"你好", "draft"}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
	if cmd == nil || !reflect.DeepEqual(cmd(), tea.SetClipboard("你好\ndraft")()) {
		t.Fatal("Cmd+C 应复制完整草稿到剪贴板")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModSuper})
	if cmd == nil || !reflect.DeepEqual(cmd(), tea.ReadClipboard()) {
		t.Fatal("Cmd+V 应请求读取剪贴板")
	}
	m.Update(tea.ClipboardMsg{Selection: 'c', Content: "甲\n乙"})
	if want := []string{"甲", "乙你好", "draft"}; !reflect.DeepEqual(m.lines, want) || m.row != 1 || m.col != 1 {
		t.Fatalf("剪贴板应在光标处插入，got %q (%d,%d)", m.lines, m.row, m.col)
	}
	if m.clipboardPending {
		t.Fatal("读取剪贴板后应清除等待状态")
	}
}

func TestUpdateIgnoresPasteWhileBusy(t *testing.T) {
	for _, p := range []phase{phaseSending, phaseStreaming} {
		m, _, _ := newTestModel()
		m.phase, m.lines = p, []string{"draft"}
		for _, msg := range []tea.Msg{
			tea.PasteMsg{Content: "pasted\ntext"},
			tea.KeyPressMsg{Code: 'v', Mod: tea.ModSuper},
			tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper},
			tea.ClipboardMsg{Selection: 'c', Content: "clipboard"},
		} {
			m.clipboardPending = true
			_, cmd := m.Update(msg)
			if cmd != nil || !reflect.DeepEqual(m.lines, []string{"draft"}) {
				t.Fatalf("phase=%v 不应处理剪贴板编辑，msg=%T lines=%q", p, msg, m.lines)
			}
		}
		_, cmd := m.Update(keyCtrlC())
		if cmd == nil || !reflect.DeepEqual(cmd(), tea.Quit()) {
			t.Fatal("忙碌时 Ctrl+C 仍应退出")
		}
	}
}

func TestUpdateIgnoresClipboardAfterSubmit(t *testing.T) {
	m, _, _ := newTestModel()
	m.insert("draft")
	m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModSuper})
	m.Update(keyEnter())
	m.Update(inChunkMsg{text: StreamEnd})
	m.Update(tea.ClipboardMsg{Selection: 'c', Content: "late clipboard"})
	if !reflect.DeepEqual(m.lines, []string{""}) {
		t.Fatalf("上一轮的剪贴板响应不应插入下一轮输入，got %q", m.lines)
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

func TestViewRendersStreamBufferAndSeparators(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 30
	m.streamBuf = "[LaxCode] thinking: ..."
	content := m.View().Content

	if !strings.Contains(content, "[LaxCode] thinking: ...") {
		t.Error("View 应实时显示正在流式的半行")
	}
	if n := strings.Count(content, strings.Repeat("─", 30)); n != 2 {
		t.Errorf("应有两条等宽实线分隔符，got %d", n)
	}
	if !strings.Contains(content, "> ") {
		t.Error("View 应包含固定在第一行的提示符 '> '")
	}
	// 流式半行应显示在输入区分隔符上方（更早出现）
	if strings.Index(content, "[LaxCode]") > strings.Index(content, strings.Repeat("─", 30)) {
		t.Error("流式半行应显示在输入区分隔符上方")
	}
}

func TestViewOmitsEmptyStreamBuffer(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 10
	content := m.View().Content
	// streamBuf 为空时 View 应直接以分隔符开头，不多出前导空行
	if !strings.HasPrefix(content, strings.Repeat("─", 10)) {
		t.Errorf("空 streamBuf 时 View 应以分隔符开头，got %q", content)
	}
}

func TestFitHeightCapsToTerminalHeight(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 3
	if got := m.fitHeight("l1\nl2\nl3\nl4\nl5"); got != "l3\nl4\nl5" {
		t.Errorf("应只保留底部 3 行，got %q", got)
	}
}

func TestFitHeightKeepsContentWhenFits(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 10
	if got := m.fitHeight("l1\nl2"); got != "l1\nl2" {
		t.Errorf("未超高应原样返回，got %q", got)
	}
}

func TestFitHeightNoCapWhenHeightUnknown(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 0
	if got := m.fitHeight("l1\nl2\nl3"); got != "l1\nl2\nl3" {
		t.Errorf("高度未知时应原样返回，got %q", got)
	}
}

// TestViewStaysSmallAsHistoryGrows 同时是防闪烁与"历史可上翻"的回归：多轮流式后，
// 已完成行必须被切出（打印到 scrollback），而不是堆在 View 里——View 逻辑行数应恒定
// 不超屏，且不含任何已完成的历史行。
func TestViewStaysSmallAsHistoryGrows(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 40
	m.height = 6
	m.phase = phaseStreaming
	for i := 0; i < 50; i++ {
		m.Update(inChunkMsg{text: "assistant line\n"})
	}
	content := m.View().Content
	if h := strings.Count(content, "\n") + 1; h > m.height {
		t.Errorf("View 逻辑行数=%d 超过终端高度 %d（历史应进 scrollback 而非堆在 View）", h, m.height)
	}
	if strings.Contains(content, "assistant line") {
		t.Error("已完成的历史行不应留在 View（应已切出打印到 scrollback）")
	}
	if m.streamBuf != "" {
		t.Errorf("每个 chunk 都以 \\n 结束，streamBuf 应始终为空，got %q", m.streamBuf)
	}
}

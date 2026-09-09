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
func keyCtrlA() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl} }
func keyCtrlE() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl} }
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

// TestUpdateCtrlAGoesToLineStart 验证 Ctrl+A 跳到当前行行首且幂等，不返回命令、不改文本。
func TestUpdateCtrlAGoesToLineStart(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"hello world"}
	m.row, m.col = 0, 7
	if _, cmd := m.Update(keyCtrlA()); cmd != nil {
		t.Error("Ctrl+A 不应返回命令")
	}
	if m.row != 0 || m.col != 0 {
		t.Fatalf("Ctrl+A 后光标=(%d,%d)，期望 (0,0)", m.row, m.col)
	}
	if m.lines[0] != "hello world" {
		t.Errorf("跳转不应改动文本，got %q", m.lines[0])
	}
	// 已处行首再按应保持幂等
	m.Update(keyCtrlA())
	if m.col != 0 {
		t.Errorf("行首再按 Ctrl+A 应保持 col=0，got %d", m.col)
	}
}

// TestUpdateCtrlEGoesToLineEnd 验证 Ctrl+E 跳到当前行行尾（rune 计）且幂等（含 CJK）。
func TestUpdateCtrlEGoesToLineEnd(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"你好abc"} // 2 个 CJK + 3 个 ASCII = 5 个 rune
	m.row, m.col = 0, 1
	if _, cmd := m.Update(keyCtrlE()); cmd != nil {
		t.Error("Ctrl+E 不应返回命令")
	}
	if m.row != 0 || m.col != 5 {
		t.Fatalf("Ctrl+E 后光标=(%d,%d)，期望 (0,5)", m.row, m.col)
	}
	if m.lines[0] != "你好abc" {
		t.Errorf("跳转不应改动文本，got %q", m.lines[0])
	}
	m.Update(keyCtrlE())
	if m.col != 5 {
		t.Errorf("行尾再按 Ctrl+E 应保持 col=5，got %d", m.col)
	}
}

// TestCtrlAEStaysOnCurrentRow 验证 Ctrl+A/E 是行级跳转：只动 col 不动 row，
// 光标在其他行时不会跨行跳回文档首/尾。
func TestCtrlAEStaysOnCurrentRow(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"first line", "second"}
	m.row, m.col = 0, 3
	m.Update(keyCtrlE()) // 第 0 行行尾
	if m.row != 0 || m.col != len([]rune("first line")) {
		t.Fatalf("第 0 行 Ctrl+E 应停在行尾，got (%d,%d)", m.row, m.col)
	}
	m.Update(keyCtrlA()) // 第 0 行行首
	if m.row != 0 || m.col != 0 {
		t.Fatalf("第 0 行 Ctrl+A 应停在行首，got (%d,%d)", m.row, m.col)
	}
	m.row, m.col = 1, 2
	m.Update(keyCtrlA())
	if m.row != 1 || m.col != 0 {
		t.Fatalf("第 1 行 Ctrl+A 应停在 (1,0)，got (%d,%d)", m.row, m.col)
	}
	m.Update(keyCtrlE())
	if m.row != 1 || m.col != len([]rune("second")) {
		t.Fatalf("第 1 行 Ctrl+E 应停在行尾，got (%d,%d)", m.row, m.col)
	}
	m.View() // 跳转后渲染应安全
}

// TestUpdateIgnoresCtrlAEWhileNotInput 验证发送/流式阶段 Ctrl+A/E 不生效。
func TestUpdateIgnoresCtrlAEWhileNotInput(t *testing.T) {
	for _, p := range []phase{phaseSending, phaseStreaming} {
		m, _, _ := newTestModel()
		m.phase, m.lines = p, []string{"abcdef"}
		m.row, m.col = 0, 3
		for _, k := range []tea.KeyPressMsg{keyCtrlA(), keyCtrlE()} {
			_, cmd := m.Update(k)
			if cmd != nil {
				t.Fatalf("phase=%v 不应因 %q 返回命令", p, k.String())
			}
			if m.col != 3 || m.lines[0] != "abcdef" {
				t.Fatalf("phase=%v 下 %q 不应移动光标或改文本，col=%d", p, k.String(), m.col)
			}
		}
	}
}

// TestCtrlShiftAIsNotLineStart 防回归：Ctrl+Shift+A（全选）String() 为 "ctrl+shift+a"，
// 不应误命中 Ctrl+A 触发跳转或插入文本。
func TestCtrlShiftAIsNotLineStart(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"abcdef"}
	m.row, m.col = 0, 3
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModShift})
	if cmd != nil {
		t.Error("Ctrl+Shift+A 不应返回命令")
	}
	if m.col != 3 || m.lines[0] != "abcdef" {
		t.Fatalf("Ctrl+Shift+A 不应移动光标或改文本，col=%d lines=%q", m.col, m.lines[0])
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

func TestClipLinesCapsToTerminalHeight(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 3
	kept, dropped := m.clipLines([]string{"l1", "l2", "l3", "l4", "l5"})
	if got := strings.Join(kept, "\n"); got != "l3\nl4\nl5" {
		t.Errorf("应只保留底部 3 行，got %q", got)
	}
	if dropped != 2 {
		t.Errorf("应报告丢弃 2 行（View 需据此下移光标行号），got %d", dropped)
	}
}

func TestClipLinesKeepsContentWhenFits(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 10
	kept, dropped := m.clipLines([]string{"l1", "l2"})
	if got := strings.Join(kept, "\n"); got != "l1\nl2" || dropped != 0 {
		t.Errorf("未超高应原样返回，got %q dropped=%d", got, dropped)
	}
}

func TestClipLinesNoCapWhenHeightUnknown(t *testing.T) {
	m, _, _ := newTestModel()
	m.height = 0
	kept, dropped := m.clipLines([]string{"l1", "l2", "l3"})
	if len(kept) != 3 || dropped != 0 {
		t.Errorf("高度未知时应原样返回，got %q dropped=%d", kept, dropped)
	}
}

// TestInputViewHasNoReverseVideo 防回归：光标改由终端硬件光标承担后，输入区不得再出现
// 行内反色（SGR 7）——inline 差分渲染下反色高亮是宽字符行残留高亮块的根因。
func TestInputViewHasNoReverseVideo(t *testing.T) {
	m, _, _ := newTestModel()
	m.lines = []string{"你好abc"}
	m.row, m.col = 0, 1
	if got := m.inputView(); got != "> 你好abc" {
		t.Errorf("inputView 应输出无反色转义的纯文本，got %q", got)
	}
	if got := m.inputView(); strings.Contains(got, "\x1b[7m") {
		t.Errorf("inputView 不应包含反色高亮，got %q", got)
	}
}

// TestViewCursorAtWideCharColumn 验证硬件光标列号按显示宽度计算（CJK 占 2 列），
// 行号为「分隔线 + 光标所在输入行」。
func TestViewCursorAtWideCharColumn(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.lines = []string{"你好abc"}
	m.row, m.col = 0, 2 // 光标在 "abc" 之前
	c := m.View().Cursor
	if c == nil {
		t.Fatal("应设置硬件光标")
	}
	if c.X != promptWidth+4 || c.Y != 1 {
		t.Fatalf("光标=(%d,%d)，期望 (%d,1)", c.X, c.Y, promptWidth+4)
	}
}

// TestViewCursorAtLineEnd 验证行尾（col == 行 rune 数）时光标落在文本右侧的空白单元格。
func TestViewCursorAtLineEnd(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.lines = []string{"你好"}
	m.row, m.col = 0, 2
	c := m.View().Cursor
	if c == nil {
		t.Fatal("行尾也应设置硬件光标")
	}
	if c.X != promptWidth+4 {
		t.Errorf("行尾光标列=%d，期望 %d（文本右侧空白单元格）", c.X, promptWidth+4)
	}
}

// TestViewCursorIsSteadyBlock 验证光标为稳定（不闪烁）块状：贴近原反色块的观感，
// 且非终端默认形状时 bubbletea 退出时会把光标形状还原为终端默认。
func TestViewCursorIsSteadyBlock(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.insert("hi")
	c := m.View().Cursor
	if c == nil {
		t.Fatal("应设置硬件光标")
	}
	if c.Shape != tea.CursorBlock || c.Blink {
		t.Errorf("光标应为稳定块状，got shape=%v blink=%v", c.Shape, c.Blink)
	}
}

// TestViewCursorAccountsForStreamLine 验证流式半行占据帧首行时光标行号随之下移。
func TestViewCursorAccountsForStreamLine(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.streamBuf = "thinking..."
	m.lines = []string{"draft"}
	m.row, m.col = 0, 5
	c := m.View().Cursor
	if c == nil {
		t.Fatal("应设置硬件光标")
	}
	if c.X != promptWidth+5 || c.Y != 2 {
		t.Fatalf("光标=(%d,%d)，期望 (%d,2)", c.X, c.Y, promptWidth+5)
	}
}

// TestViewCursorTracksRowAndClipping 验证多行输入时光标跟随当前行，且超高裁剪后行号按
// 丢弃行数下移；光标行本身被裁掉时不设置光标（避免指向帧外）。
func TestViewCursorTracksRowAndClipping(t *testing.T) {
	m, _, _ := newTestModel()
	m.width = 40
	m.lines = []string{"a", "b", "c"} // 帧共 5 行：分隔线 + 3 输入行 + 分隔线
	m.row, m.col = 1, 1
	m.height = 10
	if c := m.View().Cursor; c == nil || c.Y != 2 {
		t.Fatalf("未裁剪时第二行光标行号应为 2，got %#v", c)
	}

	m.height = 4 // 5 行裁到 4 行，丢弃顶部 1 行
	m.row, m.col = 2, 1
	c := m.View().Cursor
	if c == nil {
		t.Fatal("光标行仍在帧内，应设置硬件光标")
	}
	if c.Y != 2 { // 裁剪前 1+2=3，丢弃 1 行 → 2
		t.Errorf("裁剪后光标行=%d，期望 2", c.Y)
	}

	m.height = 2 // 只剩 [输入区末行, 底部分隔线]，光标行被裁掉
	m.row, m.col = 0, 0
	if c := m.View().Cursor; c != nil {
		t.Errorf("光标行被裁掉时不应设置硬件光标，got (%d,%d)", c.X, c.Y)
	}
}

// TestCursorMoveKeepsContentStable 是改用硬件光标的核心回归：光标移动（四个方向键与
// Ctrl+A/E）只改 View.Cursor，绝不改 View.Content——inline 差分渲染因此没有任何单元格
// 需要重写（含宽字符的行也不会有「清除旧反色」的写入），残留高亮块从源头消失。
func TestCursorMoveKeepsContentStable(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.lines = []string{"这是一段很长的中文", "second"} // 首行 9 个宽字符 = 18 列
	m.row, m.col = 0, len([]rune(m.lines[0]))
	want := m.View().Content
	if v := m.View(); v.Cursor == nil || v.Cursor.X != promptWidth+18 || v.Cursor.Y != 1 {
		t.Fatalf("初始光标应在行尾 (%d,1)，got %#v", promptWidth+18, v.Cursor)
	}

	for _, step := range []struct {
		name         string
		msg          tea.KeyPressMsg
		wantX, wantY int
	}{
		{"left", tea.KeyPressMsg{Code: tea.KeyLeft}, promptWidth + 16, 1}, // 宽字符左移一格 = 2 列
		{"ctrl+a", keyCtrlA(), promptWidth, 1},                            // 行首
		{"ctrl+e", keyCtrlE(), promptWidth + 18, 1},                       // 行尾
		{"down", tea.KeyPressMsg{Code: tea.KeyDown}, promptWidth + 6, 2},  // col 夹到第二行行尾
		{"up", tea.KeyPressMsg{Code: tea.KeyUp}, promptWidth + 12, 1},     // col=6 → 6 个宽字符 = 12 列
	} {
		m.Update(step.msg)
		v := m.View()
		if v.Content != want {
			t.Fatalf("%s 后 View 内容变化（光标移动不应重写任何单元格）:\n got %q\nwant %q", step.name, v.Content, want)
		}
		if v.Cursor == nil {
			t.Fatalf("%s 后应仍有硬件光标", step.name)
		}
		if v.Cursor.X != step.wantX || v.Cursor.Y != step.wantY {
			t.Errorf("%s 后光标=(%d,%d)，期望 (%d,%d)", step.name, v.Cursor.X, v.Cursor.Y, step.wantX, step.wantY)
		}
		if strings.Contains(v.Content, "\x1b[7m") {
			t.Fatalf("%s 后内容不得出现反色转义，got %q", step.name, v.Content)
		}
	}
}

// TestViewCursorOnEmptyInputArea 验证输入区为空（无行）时不设置光标，View 仍安全返回。
func TestViewCursorOnEmptyInputArea(t *testing.T) {
	m, _, _ := newTestModel()
	m.width, m.height = 40, 10
	m.lines = nil
	v := m.View()
	if v.Cursor != nil {
		t.Errorf("输入区为空时不应设置光标，got (%d,%d)", v.Cursor.X, v.Cursor.Y)
	}
	if n := strings.Count(v.Content, "─"); n < 2 {
		t.Errorf("仍应画出上下分隔线，got %q", v.Content)
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

// Package cliprinter 提供交互模式（cmd/run_cli）的终端 UI：基于 bubbletea 的
// 多行输入框 + 流式回复渲染。用户输入经 OutChan 交给上层调用 Chat，上层的
// ReAct 事件与每轮结束标志经 InChan 回流。已完成的历史（用户输入回显、对端整行）
// 用 tea.Println 打印到终端 scrollback（受管视图上方、可上翻且持久保留），受管视图
// 只保留正在流式的半行与输入区，行数恒定：既避免超屏每帧重绘闪烁，又保留完整可翻历史。
package cliprinter

import (
	"errors"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// StreamEnd 是 InChan 的流式终止符（EOT，U+0004），不会出现在正常文本中。
// 上层在一轮回复结束后必须单独发送它一次，TUI 收到后回到用户输入阶段。
const StreamEnd = "\x04"

// phase 表示交互阶段，用于控制何时接受用户输入编辑。
type phase int

const (
	phaseInput     phase = iota // 等待并接收用户输入
	phaseSending                // 正在把用户输入写入 OutChan（阻塞等待上层消费）
	phaseStreaming              // 正在从 InChan 流式接收对端回复
)

// outFlushedMsg 表示用户输入已写入 OutChan 且被上层消费。
type outFlushedMsg struct{}

// inChunkMsg 表示从 InChan 读到的一段回复内容。
type inChunkMsg struct{ text string }

// inClosedMsg 表示 InChan 已关闭（上层结束）。
type inClosedMsg struct{}

// sendOut 在后台把 text 阻塞写入 out，被消费后返回 outFlushedMsg。阻塞发生在
// bubbletea 的 Cmd goroutine，不会卡住 UI 事件循环。
func sendOut(out chan<- string, text string) tea.Cmd {
	return func() tea.Msg {
		out <- text // 阻塞等待上层消费
		return outFlushedMsg{}
	}
}

// readIn 在后台从 in 阻塞读取一段内容；读到返回 inChunkMsg，通道关闭返回 inClosedMsg。
func readIn(in <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-in
		if !ok {
			return inClosedMsg{}
		}
		return inChunkMsg{text: text}
	}
}

// model 是 bubbletea 的 Model：保存多行输入内容、光标坐标（row/col 以 rune 计）、
// 终端宽高、交互阶段、流式缓冲（streamBuf）与收发通道。已完成内容不留在 model，
// 而是即时打印到终端 scrollback（见 appendStream/flushStream）。
type model struct {
	lines            []string // 输入区按行保存，行内按 rune 处理
	row, col         int      // 光标位置：lines[row] 中第 col 个 rune 之前
	width            int      // 终端宽度（列数），用于绘制与屏幕等宽的分隔线
	height           int      // 终端高度（行数）：fitHeight 安全网，正常历史走 scrollback 不依赖它
	phase            phase    // 当前交互阶段
	streamBuf        string   // 正在流式接收、尚未遇到换行的尾部：完整行即时打印到 scrollback，尾部半行由 View 实时显示
	outChan          chan<- string
	inChan           <-chan string
	clipboardPending bool // 等待由 Cmd+V 发起的剪贴板读取结果
}

// Init 实现 tea.Model：启动时无需执行命令。
func (m *model) Init() tea.Cmd { return nil }

// insert 在光标处插入文本（不含有换行符的单段文本）。
func (m *model) insert(text string) {
	runes := []rune(m.lines[m.row])
	runes = append(runes[:m.col], append([]rune(text), runes[m.col:]...)...)
	m.lines[m.row] = string(runes)
	m.col += len([]rune(text))
}

// paste 在光标处插入整段文本；换行只拆分输入行，不触发发送。
func (m *model) paste(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			m.newline()
		}
		m.insert(line)
	}
}

// deleteBackspace 删除光标前一个 rune；若在行首则与上一行合并。
func (m *model) deleteBackspace() {
	if m.col > 0 {
		runes := []rune(m.lines[m.row])
		m.lines[m.row] = string(runes[:m.col-1]) + string(runes[m.col:])
		m.col--
		return
	}
	if m.row > 0 {
		m.col = len([]rune(m.lines[m.row-1]))
		m.lines[m.row-1] += m.lines[m.row]
		m.lines = append(m.lines[:m.row], m.lines[m.row+1:]...)
		m.row--
	}
}

// newline 在光标处把当前行拆成两行（alt+enter 的换行）。
func (m *model) newline() {
	runes := []rune(m.lines[m.row])
	tail := string(runes[m.col:])
	m.lines[m.row] = string(runes[:m.col])
	m.lines = append(m.lines[:m.row+1], append([]string{tail}, m.lines[m.row+1:]...)...)
	m.row++
	m.col = 0
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// 记录终端宽高，供 View 绘制等宽分隔线与限制高度使用
		m.width = msg.Width
		m.height = msg.Height
	case outFlushedMsg:
		// OutChan 已被上层消费：开始从 InChan 流式读取本轮回复
		m.phase = phaseStreaming
		return m, readIn(m.inChan)
	case inChunkMsg:
		if msg.text == StreamEnd {
			// 收到终止符：本轮回复结束，flush 残留半行后回到用户输入阶段
			m.phase = phaseInput
			return m, m.flushStream()
		}
		// 累积到 streamBuf：切出的完整行即时打印到 scrollback（可上翻），尾部半行留在
		// View 实时显示；随后继续读取下一段。用 Sequence 保证多行按序打印且先于下一次
		// 读取，避免 Batch 的并发乱序。
		cmds := m.appendStream(msg.text)
		cmds = append(cmds, readIn(m.inChan))
		return m, tea.Sequence(cmds...)
	case inClosedMsg:
		// InChan 已关闭：flush 残留半行，回到用户输入阶段
		m.phase = phaseInput
		return m, m.flushStream()
	case tea.PasteMsg:
		// 终端通常自行处理 Cmd+V，再通过 bracketed paste 发送文本。
		if m.phase == phaseInput {
			m.paste(msg.Content)
		}
	case tea.ClipboardMsg:
		if m.clipboardPending && msg.Selection == 'c' {
			m.clipboardPending = false
			if m.phase == phaseInput {
				m.paste(msg.Content)
			}
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// 发送/接收阶段不接受编辑输入，避免与流式回复交错
		if m.phase != phaseInput {
			return m, nil
		}
		switch msg.String() {
		case "super+c":
			// 支持将 Command 键直接上报的终端；输入框没有内部选区，复制整个草稿。
			// 普通终端的鼠标选择 + Cmd+C 由终端自身处理。
			if input := strings.Join(m.lines, "\n"); input != "" {
				return m, tea.SetClipboard(input)
			}
		case "super+v":
			// 直接上报按键时通过 OSC52 读取（需要终端支持）；普通粘贴走 PasteMsg。
			if !m.clipboardPending {
				m.clipboardPending = true
				return m, tea.ReadClipboard
			}
		case "enter":
			m.clipboardPending = false
			input := strings.Join(m.lines, "\n")
			if strings.TrimSpace(input) == "" {
				// 空输入不发送，仅清空输入区（对齐原 CLI 跳过空行的行为）
				m.lines = []string{""}
				m.row, m.col = 0, 0
				return m, nil
			}
			m.lines = []string{""}
			m.row, m.col = 0, 0
			m.phase = phaseSending
			// 用户输入以浅灰背景+黑字回显到 scrollback（可上翻），随后阻塞写入 OutChan
			// 等待上层消费。用 Sequence 保证“先回显、后发送”的顺序。
			echo := tea.Println(strings.TrimSuffix(m.userMessageView(input), "\n"))
			return m, tea.Sequence(echo, sendOut(m.outChan, input))
		case "alt+enter":
			m.newline()
		case "up":
			if m.row > 0 {
				m.row--
				m.col = min(m.col, len([]rune(m.lines[m.row])))
			}
		case "down":
			if m.row < len(m.lines)-1 {
				m.row++
				m.col = min(m.col, len([]rune(m.lines[m.row])))
			}
		case "left":
			if m.col > 0 {
				m.col--
			} else if m.row > 0 {
				m.row--
				m.col = len([]rune(m.lines[m.row]))
			}
		case "right":
			if m.col < len([]rune(m.lines[m.row])) {
				m.col++
			} else if m.row < len(m.lines)-1 {
				m.row++
				m.col = 0
			}
		case "backspace":
			m.deleteBackspace()
		default:
			// KeyPressMsg.String() 对可打印字符返回字符本身（空格返回 "space"），
			// 直接用 Text 拿原始文本更稳，可跳过 IME/组合键产生的控制字符。
			if msg.Text != "" {
				m.insert(msg.Text)
			}
		}
	}
	return m, nil
}

// termWidth 返回终端列数，未知时回退到默认 80 列。
func (m *model) termWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

// hline 返回一条与终端等宽的实线分隔符（Unicode 制表符 U+2500）。
func (m *model) hline() string {
	return strings.Repeat("─", m.termWidth())
}

// fitHeight 把内容裁剪到不超过终端高度的逻辑行数，只保留底部（最近的消息与输入
// 区）。bubbletea 标准渲染器在内容高于屏幕时会丢弃顶部行并每帧全量重绘，导致
// 持续闪烁，故必须限高。超宽行由渲染器截断（非折叠），逻辑行数即视觉行数；
// 且各行内 ANSI 均自成一段（以 reset 收尾），按行裁剪不会破坏样式状态。
func (m *model) fitHeight(content string) string {
	if m.height <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= m.height {
		return content
	}
	return strings.Join(lines[len(lines)-m.height:], "\n")
}

// appendStream 把 chunk 追加到 streamBuf，并切出其中所有完整行（以 \n 结束）分别作为
// tea.Println 命令返回——它们把已完成内容打印到终端 scrollback（受管视图上方，可上翻、
// 持久保留）。未结束的尾部留在 streamBuf，由 View 实时显示。空行跳过（insertAbove 对空
// 串是 no-op，且与“不渲染空历史行”的既有行为一致）。
func (m *model) appendStream(chunk string) []tea.Cmd {
	m.streamBuf += chunk
	var cmds []tea.Cmd
	for {
		i := strings.IndexByte(m.streamBuf, '\n')
		if i < 0 {
			break
		}
		line := m.streamBuf[:i]
		m.streamBuf = m.streamBuf[i+1:]
		if line != "" {
			cmds = append(cmds, tea.Println(line))
		}
	}
	return cmds
}

// flushStream 把 streamBuf 中残留的半行（本轮结束时未以换行收尾的尾部）打印到
// scrollback 并清空缓冲；无残留时返回 nil。
func (m *model) flushStream() tea.Cmd {
	if m.streamBuf == "" {
		return nil
	}
	cmd := tea.Println(m.streamBuf)
	m.streamBuf = ""
	return cmd
}

// userMessageView 把用户输入渲染成浅灰背景+黑字，并按显示宽度补空格让背景铺满整行（多行逐行处理）。
func (m *model) userMessageView(text string) string {
	w := m.termWidth()
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		pad := w - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		// 浅灰背景（256 色 250）+ 黑色字色（30），行尾用空格填满至等宽
		b.WriteString("\x1b[48;5;250m\x1b[30m" + line + strings.Repeat(" ", pad) + "\x1b[0m\n")
	}
	return b.String()
}

// inputView 渲染输入区：提示符 ">" 固定在输入区第一行（不跟随光标行），
// 光标所在字符用反色块高亮（行尾高亮空白），其余行缩进与 "> " 对齐。
func (m *model) inputView() string {
	var b strings.Builder
	for i, line := range m.lines {
		// ">" 只出现在第一行，其余行用等宽空格缩进对齐
		if i == 0 {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		if i == m.row {
			// 光标所在行：反色显示光标处字符，行尾高亮一个空格占位
			runes := []rune(line)
			b.WriteString(string(runes[:m.col]))
			if m.col < len(runes) {
				b.WriteString("\x1b[7m" + string(runes[m.col]) + "\x1b[0m")
				b.WriteString(string(runes[m.col+1:]))
			} else {
				b.WriteString("\x1b[7m \x1b[0m")
			}
		} else {
			b.WriteString(line)
		}
		if i < len(m.lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m *model) View() tea.View {
	var b strings.Builder
	// 已完成的历史（用户输入回显、对端整行）都已打印到 scrollback，可上翻；View 只保留
	// 正在流式的半行 + 输入区，行数恒定不超屏，从根本上避免渲染器每帧全量重绘闪烁。
	if m.streamBuf != "" {
		// 正在流式接收、尚未换行的尾部：实时显示在输入区上方（完整后即滚入 scrollback）
		b.WriteString(m.streamBuf + "\n")
	}
	// 输入区上下各画一条与屏幕等宽的实线分隔符
	b.WriteString(m.hline() + "\n")
	b.WriteString(m.inputView() + "\n")
	b.WriteString(m.hline())
	// 安全网：多行输入过高时仍限制逻辑行数不超过终端高度，避免超屏重绘
	return tea.NewView(m.fitHeight(b.String()))
}

// TUI 封装 bubbletea Program，对外提供阻塞式 Run。它只持有 send 端的 OutChan
// 与 receive 端的 InChan，二者的生产/消费由调用方（cmd/run_cli）负责。
type TUI struct {
	program *tea.Program
}

// NewTUI 创建一个 TUI：用户输入写入 outChan，对端回复从 inChan 流式读入，
// 读到 StreamEnd 结束一轮并回到用户输入。
func NewTUI(outChan chan<- string, inChan <-chan string) *TUI {
	m := &model{lines: []string{""}, outChan: outChan, inChan: inChan}
	return &TUI{program: tea.NewProgram(m)}
}

// Run 阻塞运行 TUI，直到用户按 ctrl+c 或进程收到 SIGINT/SIGTERM。这些中断都
// 视为正常退出（返回 nil）；仅底层 Program 的其他错误才返回非 nil。返回时终端
// 已由 bubbletea 恢复到正常模式。
func (t *TUI) Run() error {
	if _, err := t.program.Run(); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	return nil
}

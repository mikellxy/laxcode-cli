// Package cliprinter 提供交互模式（cmd/run_cli）的终端 UI：基于 bubbletea 的
// 多行输入框 + 流式回复渲染。用户输入经 OutChan 交给上层调用 Chat，上层的
// ReAct 事件与每轮结束标志经 InChan 回流。已完成的历史（用户输入回显、对端整行）
// 用 tea.Println 打印到终端 scrollback（受管视图上方、可上翻且持久保留），受管视图
// 只保留正在流式的半行与输入区，行数恒定：既避免超屏每帧重绘闪烁，又保留完整可翻历史。
// 输入光标由终端硬件光标（View.Cursor）呈现，不在内容里画反色块：inline 模式的单元格
// 差分渲染在含宽字符的行上清除反色易打偏，按住方向键会留下成片残留高亮。
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
	lines     []string // 输入区按行保存，行内按 rune 处理
	row, col  int      // 光标位置：lines[row] 中第 col 个 rune 之前
	width     int      // 终端宽度（列数），用于绘制与屏幕等宽的分隔线
	height    int      // 终端高度（行数）：clipLines 安全网，正常历史走 scrollback 不依赖它
	phase     phase    // 当前交互阶段
	streamBuf string   // 正在流式接收、尚未遇到换行的尾部：完整行即时打印到 scrollback，尾部半行由 View 实时显示
	outChan   chan<- string
	inChan    <-chan string
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

// startOfLine 把光标移到当前行行首（row 不变，col=0）。
func (m *model) startOfLine() { m.col = 0 }

// endOfLine 把光标移到当前行行尾（row 不变，col 以 rune 计）。
func (m *model) endOfLine() { m.col = len([]rune(m.lines[m.row])) }

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
		// 粘贴统一由终端通过 bracketed paste 发送；不主动查询 OSC52 剪贴板。
		if m.phase == phaseInput {
			m.paste(msg.Content)
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
		case "enter":
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
		case "ctrl+a":
			// 跳到当前行行首（readline 惯例）；Ctrl+Shift+A（全选）不会误命中，
			// 其 String() 为 "ctrl+shift+a"。
			m.startOfLine()
		case "ctrl+e":
			// 跳到当前行行尾（readline 惯例）。
			m.endOfLine()
		case "backspace":
			m.deleteBackspace()
		default:
			// KeyPressMsg.String() 对可打印字符返回字符本身（空格返回 "space"），
			// 直接用 Text 拿原始文本更稳，可跳过 IME/组合键产生的控制字符。
			if msg.Text != "" && msg.Mod&tea.ModSuper == 0 {
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

// clipLines 把行集裁剪到不超过终端高度的逻辑行数，只保留底部（最近的消息与输入区）。
// bubbletea 标准渲染器在内容高于屏幕时会丢弃顶部行并每帧全量重绘，导致持续闪烁，
// 故必须限高。超宽行由渲染器截断（非折叠），逻辑行数即视觉行数；且各行内 ANSI 均
// 自成一段（以 reset 收尾），按行裁剪不会破坏样式状态。
//
// 返回保留的行与实际丢弃的行数：View 需要丢弃数把硬件光标行号同步下移。高度未知
// （0）或未超高时原样返回，dropped 为 0。
func (m *model) clipLines(lines []string) (kept []string, dropped int) {
	if m.height <= 0 || len(lines) <= m.height {
		return lines, 0
	}
	dropped = len(lines) - m.height
	return lines[dropped:], dropped
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

// inputView 渲染输入区：提示符 ">" 固定在输入区第一行（不跟随光标行），其余行缩进
// 与 "> " 对齐。输出为纯文本，不含任何光标绘制。
//
// 光标不在这里画，而由 View 交给终端硬件光标（View.Cursor）。早期实现用行内反色
// （SGR 7）高亮光标字符：inline 模式下 bubbletea 对 View 内容做单元格级差分渲染，
// 反色高亮意味着每帧都要重写「新高亮 + 清除旧高亮」两个字符；含宽字符（CJK）的行
// 在这种差分下清除写容易打偏（依赖渲染器光标模型与终端宽字符擦除语义严格一致），
// 按住方向键时会累积出成片残留反色块。硬件光标移动不改动任何单元格，帧间差分天然
// 为零，从源头消除该问题。
func (m *model) inputView() string {
	var b strings.Builder
	for i, line := range m.lines {
		// ">" 只出现在第一行，其余行用等宽空格缩进对齐
		if i == 0 {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
		if i < len(m.lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// promptWidth 是输入行提示符（"> " / "  "）占用的列数。
const promptWidth = 2

// cursorCell 返回硬件光标在帧内的坐标：inputStart 为输入区首行在帧内的行号（裁剪前）。
// 列号 = 提示符宽度 + 光标前文本的显示宽度（宽字符按 2 列计）；位于行尾时光标落在
// 文本右侧的空白单元格上。ok 为 false 表示输入区为空、无处安放光标（此时不设置
// View.Cursor，保持光标隐藏）。
//
// row/col 先夹到合法区间：View 必须永不 panic（调用方可能给出越界坐标）。
func (m *model) cursorCell(inputStart int) (x, y int, ok bool) {
	if len(m.lines) == 0 {
		return 0, 0, false
	}
	row := min(max(m.row, 0), len(m.lines)-1)
	runes := []rune(m.lines[row])
	col := min(max(m.col, 0), len(runes))
	return promptWidth + ansi.StringWidth(string(runes[:col])), inputStart + row, true
}

// View 组装流式缓冲行与输入区（上下各一条等宽分隔线），并把光标位置交给终端硬件光标。
//
// 帧布局（行号自 0 起）：[流式半行?] [分隔线] [输入区 len(m.lines) 行] [分隔线]，
// 故光标行号 = 输入区起始行 + 当前行；内容超高被裁剪时再按丢弃行数下移。
func (m *model) View() tea.View {
	lines := make([]string, 0, len(m.lines)+3)
	// 已完成的历史（用户输入回显、对端整行）都已打印到 scrollback，可上翻；View 只保留
	// 正在流式的半行 + 输入区，行数恒定不超屏，从根本上避免渲染器每帧全量重绘闪烁。
	if m.streamBuf != "" {
		// 正在流式接收、尚未换行的尾部：实时显示在输入区上方（完整后即滚入 scrollback）
		lines = append(lines, m.streamBuf)
	}
	// 输入区上下各画一条与屏幕等宽的实线分隔符
	lines = append(lines, m.hline())
	inputStart := len(lines)
	lines = append(lines, strings.Split(m.inputView(), "\n")...)
	lines = append(lines, m.hline())

	// 安全网：多行输入过高时仍限制逻辑行数不超过终端高度，避免超屏重绘；裁剪会丢弃
	// 顶部行，故光标行号同步下移，光标行本身被裁掉时不显示硬件光标（避免指向帧外）。
	lines, dropped := m.clipLines(lines)
	v := tea.NewView(strings.Join(lines, "\n"))
	if x, y, ok := m.cursorCell(inputStart); ok && y-dropped >= 0 {
		cur := tea.NewCursor(x, y-dropped)
		// 稳定（不闪烁）块状光标：贴近原来反色块的观感；且因为不是终端默认的
		// “闪烁块”（DECSCUSR 1），bubbletea 退出时会把光标形状还原为终端默认。
		cur.Blink = false
		v.Cursor = cur
	}
	return v
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

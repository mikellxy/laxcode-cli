package printer

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
)

// swapDefault 把包级默认实例切到 p 并注册恢复，包级状态共享，
// 涉及 default 的测试一律串行（不使用 t.Parallel）。
func swapDefault(t *testing.T, p Printer) {
	t.Helper()
	prev := Default()
	SetDefault(p)
	t.Cleanup(func() { SetDefault(prev) })
}

func TestWriterPrinterFormats(t *testing.T) {
	var buf bytes.Buffer
	p := NewWriterPrinter(&buf, ColorGray, ColorGreen)

	p.Printf("banner %s\n", "hi")
	p.PrintLLM(&schema.Message{ReasoningContent: "想了一下", Content: "答案是 42"})
	p.PrintToolCall("bash(ls)")
	p.PrintCompressResult(100, 20)

	want := "banner hi\n" +
		ColorGray + "[LaxCode] thinking: 想了一下" + ColorReset + "\n" +
		ColorGreen + "[LaxCode] LLM generates: 答案是 42" + ColorReset + "\n" +
		ColorYellow + "[LaxCode] tool execute... bash(ls)" + ColorReset + "\n" +
		ColorYellow + "[context compressed result] 100 input tokens, 20 output tokens" + ColorReset + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("format mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestPrintLLMSkipsEmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	p := NewWriterPrinter(&buf, ColorGray, ColorGreen)
	p.PrintLLM(&schema.Message{})
	if buf.Len() != 0 {
		t.Fatalf("empty message should print nothing, got %q", buf.String())
	}
}

func TestWithColorsDerivesSameDestinationDifferentColors(t *testing.T) {
	var buf bytes.Buffer
	main := NewWriterPrinter(&buf, ColorGray, ColorGreen)
	sub := main.WithColors(ColorPurple, ColorPurple)

	sub.PrintLLM(&schema.Message{Content: "子Agent"})
	want := ColorPurple + "[LaxCode] LLM generates: 子Agent" + ColorReset + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("sub colors mismatch:\n got %q\nwant %q", got, want)
	}

	// 派生实例与父实例共享目的地：父实例也能看到子实例已写的内容（同一 buf）
	main.Printf("main writes\n")
	if !strings.Contains(buf.String(), "子Agent") || !strings.Contains(buf.String(), "main writes") {
		t.Fatalf("parent and derived should share destination, buf=%q", buf.String())
	}
}

func TestDiscardPrinterSilent(t *testing.T) {
	var p Printer = DiscardPrinter{}
	p.Printf("x")
	p.PrintLLM(&schema.Message{Content: "y"})
	p.PrintToolCall("z")
	p.PrintCompressResult(1, 2)
	// 无 panic、无输出即为通过；再验证派生仍是静默
	if derived := p.WithColors(ColorGray, ColorGreen); derived == nil {
		t.Fatal("WithColors should return non-nil silent printer")
	}
}

func TestSetDefaultSwitchesPackageFunctions(t *testing.T) {
	var buf bytes.Buffer
	swapDefault(t, NewWriterPrinter(&buf, ColorGray, ColorGreen))

	Warnf("跳过坏行 %d", 3)
	Errorf("写盘失败: %v", "disk")
	Debugf("细节 %s", "x")
	Printf("banner\n")

	want := ColorYellow + "[LaxCode] 跳过坏行 3" + ColorReset + "\n" +
		ColorRed + "[LaxCode][WARN] 写盘失败: disk" + ColorReset + "\n" +
		ColorGray + "[debug] 细节 x" + ColorReset + "\n" +
		"banner\n"
	if got := buf.String(); got != want {
		t.Fatalf("package-level delegation mismatch:\n got %q\nwant %q", got, want)
	}

	// 默认实例切为静默后，包级函数随之静默
	SetDefault(DiscardPrinter{})
	Warnf("invisible")
	if got := buf.String(); got != want {
		t.Fatalf("discard default should swallow output, buf changed to %q", got)
	}
}

func TestDefaultIsMainPrinterInitially(t *testing.T) {
	p := Default()
	if _, ok := p.(*WriterPrinter); !ok {
		t.Fatalf("initial default should be a WriterPrinter, got %T", p)
	}
}

// TestConcurrentWritesDoNotInterleave 模拟并行 read_file 的调用提示并发打印：
// 多个 goroutine 经同一实例（及 WithColors 派生实例）并发写，最终行数与
// 每行完整性必须保持。
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	shared := NewWriterPrinter(&buf, ColorGray, ColorGreen)
	derived := shared.WithColors(ColorPurple, ColorPurple)

	const goroutines = 8
	const linesEach = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var p Printer = shared
			if g%2 == 0 {
				p = derived // 混用父/派生实例，验证共享写锁
			}
			for i := 0; i < linesEach; i++ {
				p.Printf("worker-%d-line-%d\n", g, i)
			}
		}(g)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != goroutines*linesEach {
		t.Fatalf("expected %d lines, got %d", goroutines*linesEach, len(lines))
	}
	for _, line := range lines {
		var g, i int
		if n, _ := fmt.Sscanf(line, "worker-%d-line-%d", &g, &i); n != 2 {
			t.Fatalf("interleaved or malformed line: %q", line)
		}
	}
}

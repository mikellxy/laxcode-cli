package cliprinter

import (
	"io"
	"os"
	"testing"
)

// 编译期契约：DefaultPrinter 满足 Printer 接口。
var _ Printer = DefaultPrinter{}

func capturePrint(t *testing.T, fn func(p Printer)) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	os.Stdout = w

	fn(NewDefaultPrinter())

	if err := w.Close(); err != nil {
		t.Fatalf("关闭管道失败: %v", err)
	}
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取管道失败: %v", err)
	}
	return string(out)
}

func TestDefaultPrinterPrint(t *testing.T) {
	out := capturePrint(t, func(p Printer) {
		p.Print("a", "b", 1)
	})
	if out != "ab1" {
		t.Errorf("Print 输出不符：%q", out)
	}
}

func TestDefaultPrinterPrintf(t *testing.T) {
	out := capturePrint(t, func(p Printer) {
		p.Printf("[%s] %d%%", "LaxCode", 100)
	})
	if out != "[LaxCode] 100%" {
		t.Errorf("Printf 输出不符：%q", out)
	}
}

func TestNewDefaultPrinter(t *testing.T) {
	if p := NewDefaultPrinter(); p == nil {
		t.Fatal("NewDefaultPrinter 返回 nil")
	}
}

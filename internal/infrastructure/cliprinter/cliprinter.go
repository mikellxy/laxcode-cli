package cliprinter

import (
	"fmt"
	"os"
)

type Printer interface {
	Print(a ...any)
	Printf(format string, a ...any)
	Fatal(a ...any)
	Fatalf(format string, a ...any)
}

type DefaultPrinter struct{}

func NewDefaultPrinter() *DefaultPrinter {
	return new(DefaultPrinter)
}

func (d DefaultPrinter) Print(a ...any) {
	fmt.Print(a...)
}

func (d DefaultPrinter) Printf(format string, a ...any) {
	fmt.Printf(format, a...)
}

func (d DefaultPrinter) Fatal(a ...any) {
	fmt.Println(a...)
	os.Exit(1)
}

func (d DefaultPrinter) Fatalf(format string, a ...any) {
	fmt.Printf(format, a...)
	os.Exit(1)
}

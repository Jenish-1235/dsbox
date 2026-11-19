// Package ui prints ds output: short status lines with symbols and colour.
package ui

import (
	"fmt"
	"io"
	"os"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

// Printer writes status lines. Colour is on only for terminals without NO_COLOR.
type Printer struct {
	w     io.Writer
	color bool
}

// New returns a Printer for f, detecting whether f is a terminal.
func New(f *os.File) Printer {
	return Printer{w: f, color: isTerminal(f) && os.Getenv("NO_COLOR") == ""}
}

// Plain returns a Printer without colour, for tests and pipes.
func Plain(w io.Writer) Printer { return Printer{w: w} }

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (p Printer) paint(code, s string) string {
	if !p.color {
		return s
	}
	return code + s + reset
}

func (p Printer) line(symbol, code, format string, a []any) {
	fmt.Fprintf(p.w, "%s %s\n", p.paint(code, symbol), fmt.Sprintf(format, a...))
}

// Title prints a bold heading.
func (p Printer) Title(format string, a ...any) {
	fmt.Fprintln(p.w, p.paint(bold, fmt.Sprintf(format, a...)))
}

// OK prints a success line.
func (p Printer) OK(format string, a ...any) { p.line("✓", green, format, a) }

// Warn prints something that needs attention but isn't broken.
func (p Printer) Warn(format string, a ...any) { p.line("!", yellow, format, a) }

// Fail prints something that is broken.
func (p Printer) Fail(format string, a ...any) { p.line("✗", red, format, a) }

// Up prints something newer that's available, such as an update.
func (p Printer) Up(format string, a ...any) { p.line("↑", cyan, format, a) }

// Item prints a bullet, for lists such as incoming changes.
func (p Printer) Item(format string, a ...any) { p.line("  •", cyan, format, a) }

// Hint prints an indented, dimmed suggestion under the previous line.
func (p Printer) Hint(format string, a ...any) {
	fmt.Fprintln(p.w, p.paint(dim, "    → "+fmt.Sprintf(format, a...)))
}

// Text prints a plain line.
func (p Printer) Text(format string, a ...any) {
	fmt.Fprintln(p.w, fmt.Sprintf(format, a...))
}

// Dim returns s dimmed, for inline use.
func (p Printer) Dim(s string) string { return p.paint(dim, s) }

package main

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// ttyReset is a soft reset: RIS (ESC c) would also wipe the scrollback.
const ttyReset = "\x1b[0m\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" +
	"\x1b[?2004l\x1b[?1l\x1b>\x1b[?7h"

// ttyGuard puts back the stdin termios saved at startup and soft-resets stdout.
type ttyGuard struct {
	state *term.State
}

// saveTTY snapshots stdin's termios before anything can change it.
func saveTTY() *ttyGuard {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return &ttyGuard{}
	}
	state, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		return &ttyGuard{}
	}
	return &ttyGuard{state: state}
}

// restore runs after the children are reaped, since they can change the tty too.
func (g *ttyGuard) restore() {
	if g.state != nil {
		_ = term.Restore(os.Stdin.Fd(), g.state)
	}
	if isTerminal(os.Stdout) {
		_, _ = os.Stdout.WriteString(ttyReset)
	}
}

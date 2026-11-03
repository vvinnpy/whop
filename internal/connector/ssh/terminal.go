package sshconn

import (
	"os"

	"golang.org/x/term"
)

// DefaultTerminalSize returns the current terminal size, or 80x24 when the
// process is not attached to a terminal.
func DefaultTerminalSize() (width, height int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}

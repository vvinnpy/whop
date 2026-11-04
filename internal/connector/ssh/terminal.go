package sshconn

import (
	"io"
	"os"
	"sync"

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

// prepareTerminalInput switches a local TTY to raw mode so control keys,
// arrow-key escape sequences, Ctrl+L, Ctrl+C, and interactive applications are
// forwarded to the remote PTY instead of being interpreted by the local line
// discipline. Non-terminal readers are returned unchanged.
func prepareTerminalInput(reader io.Reader) (io.Reader, func() error, error) {
	file, ok := reader.(*os.File)
	if !ok || file == nil || !term.IsTerminal(int(file.Fd())) {
		return reader, func() error { return nil }, nil
	}
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	var restoreErr error
	restore := func() error {
		once.Do(func() {
			restoreErr = term.Restore(int(file.Fd()), state)
		})
		return restoreErr
	}
	return reader, restore, nil
}

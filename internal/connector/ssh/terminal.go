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

// terminalMode tracks a local TTY in raw mode. Suspend is used for interactive
// prompts such as ZMODEM file selection, and Resume restores raw forwarding.
type terminalMode struct {
	mu       sync.Mutex
	file     *os.File
	original *term.State
	active   bool
}

// readerUnwrapper lets instrumented readers expose the terminal file used for
// raw-mode setup while keeping their own Read implementation in the data path.
type readerUnwrapper interface {
	UnwrapReader() io.Reader
}

// prepareTerminalInput switches a local TTY to raw mode so control keys,
// arrow-key escape sequences, Ctrl+L, Ctrl+C, and interactive applications are
// forwarded to the remote PTY instead of being interpreted by the local line
// discipline. Non-terminal readers are returned unchanged.
func prepareTerminalInput(reader io.Reader) (io.Reader, *terminalMode, error) {
	mode := &terminalMode{}
	file := terminalFile(reader)
	if file == nil || !term.IsTerminal(int(file.Fd())) {
		return reader, mode, nil
	}
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return nil, nil, err
	}
	mode.file = file
	mode.original = state
	mode.active = true
	return reader, mode, nil
}

func terminalFile(reader io.Reader) *os.File {
	for depth := 0; reader != nil && depth < 8; depth++ {
		if file, ok := reader.(*os.File); ok {
			return file
		}
		unwrapper, ok := reader.(readerUnwrapper)
		if !ok {
			return nil
		}
		reader = unwrapper.UnwrapReader()
	}
	return nil
}

func (m *terminalMode) Suspend() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || m.file == nil || m.original == nil {
		return nil
	}
	if err := term.Restore(int(m.file.Fd()), m.original); err != nil {
		return err
	}
	m.active = false
	return nil
}

func (m *terminalMode) Resume() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active || m.file == nil || m.original == nil {
		return nil
	}
	if _, err := term.MakeRaw(int(m.file.Fd())); err != nil {
		return err
	}
	m.active = true
	return nil
}

func (m *terminalMode) Restore() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || m.file == nil || m.original == nil {
		return nil
	}
	if err := term.Restore(int(m.file.Fd()), m.original); err != nil {
		return err
	}
	m.active = false
	return nil
}

package sshconn

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/ansi"
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
	fd       int
	original *term.State
	active   bool
}

// readerUnwrapper lets instrumented readers expose the terminal file used for
// raw-mode setup while keeping their own Read implementation in the data path.
type readerUnwrapper interface {
	UnwrapReader() io.Reader
}

// writerUnwrapper lets output capture wrappers expose the terminal file used
// for terminal-state resets without bypassing their own Write implementation.
type writerUnwrapper interface {
	UnwrapWriter() io.Writer
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
	mode.fd = int(file.Fd())
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

func terminalWriterFile(writer io.Writer) *os.File {
	for depth := 0; writer != nil && depth < 8; depth++ {
		if file, ok := writer.(*os.File); ok {
			return file
		}
		unwrapper, ok := writer.(writerUnwrapper)
		if !ok {
			return nil
		}
		writer = unwrapper.UnwrapWriter()
	}
	return nil
}

// resetTerminalStyle clears SGR attributes left by the full-screen TUI before
// a remote shell takes over. Vim's default Visual highlight uses reverse video,
// which can become unreadable when it inherits a dangling foreground color.
func resetTerminalStyle(writer io.Writer) {
	file := terminalWriterFile(writer)
	if file == nil || !term.IsTerminal(int(file.Fd())) {
		return
	}
	_, _ = io.WriteString(writer, ansi.ResetStyle)
}

func (m *terminalMode) size() (width, height int, ok bool) {
	if m == nil {
		return 0, 0, false
	}
	m.mu.Lock()
	active := m.active
	fd := m.fd
	m.mu.Unlock()
	if !active {
		return 0, 0, false
	}
	return terminalSize(fd)
}

func terminalSize(fd int) (width, height int, ok bool) {
	width, height, err := term.GetSize(fd)
	if err != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// watchTerminalSize keeps the remote PTY in sync with the local terminal.
// The first observed size is applied immediately, which also corrects a PTY
// size captured before the TUI handed the terminal over to the shell.
func watchTerminalSize(ctx context.Context, terminal *terminalMode, changes <-chan struct{}, apply func(width, height int) error) {
	if terminal == nil || apply == nil {
		return
	}
	lastWidth, lastHeight, ok := terminal.size()
	if ok {
		if err := apply(lastWidth, lastHeight); err != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-changes:
			if !open {
				return
			}
		}
		width, height, ok := terminal.size()
		if !ok || (width == lastWidth && height == lastHeight) {
			continue
		}
		if err := apply(width, height); err != nil {
			return
		}
		lastWidth, lastHeight = width, height
	}
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

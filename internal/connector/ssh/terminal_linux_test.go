package sshconn

import (
	"bytes"
	"io"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type testReaderUnwrapper struct {
	io.Reader
}

func (r testReaderUnwrapper) UnwrapReader() io.Reader {
	return r.Reader
}

type testWriterUnwrapper struct {
	io.Writer
}

func (w testWriterUnwrapper) UnwrapWriter() io.Writer {
	return w.Writer
}

func TestPrepareTerminalInputEnablesRawModeAndRestores(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()

	fd := int(slave.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	_, terminal, err := prepareTerminalInput(slave)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&unix.ICANON != 0 || raw.Lflag&unix.ECHO != 0 {
		t.Fatalf("terminal was not raw: lflag=%#x", raw.Lflag)
	}
	if err := terminal.Restore(); err != nil {
		t.Fatal(err)
	}
	after, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if after.Lflag != before.Lflag || after.Iflag != before.Iflag || after.Oflag != before.Oflag {
		t.Fatalf("terminal flags were not restored: before=%+v after=%+v", before, after)
	}
}

func TestPrepareTerminalInputUnwrapsInstrumentedReader(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()

	wrapped := testReaderUnwrapper{Reader: slave}
	got, terminal, err := prepareTerminalInput(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got != wrapped {
		t.Fatal("instrumented reader was replaced")
	}
	fd := int(slave.Fd())
	raw, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&unix.ICANON != 0 || raw.Lflag&unix.ECHO != 0 {
		t.Fatalf("terminal was not raw through unwrapped reader: lflag=%#x", raw.Lflag)
	}
	if err := terminal.Restore(); err != nil {
		t.Fatal(err)
	}
}

func TestResetTerminalStyleWritesThroughOutputWrapper(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()

	resetTerminalStyle(testWriterUnwrapper{Writer: slave})
	got := make([]byte, len(ansi.ResetStyle))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(master, got)
		readDone <- err
	}()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != ansi.ResetStyle {
		t.Fatalf("reset sequence = %q, want %q", got, ansi.ResetStyle)
	}
}

func TestResetTerminalStyleSkipsNonTerminal(t *testing.T) {
	var output bytes.Buffer
	resetTerminalStyle(&output)
	if output.Len() != 0 {
		t.Fatalf("non-terminal received %q", output.String())
	}
}

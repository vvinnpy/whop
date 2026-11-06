package sshconn

import (
	"io"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type testReaderUnwrapper struct {
	io.Reader
}

func (r testReaderUnwrapper) UnwrapReader() io.Reader {
	return r.Reader
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

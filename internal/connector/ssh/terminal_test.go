package sshconn

import (
	"bytes"
	"testing"
)

func TestPrepareTerminalInputNonTerminal(t *testing.T) {
	input := bytes.NewBufferString("input")
	got, terminal, err := prepareTerminalInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if got != input {
		t.Fatal("non-terminal reader was replaced")
	}
	if err := terminal.Restore(); err != nil {
		t.Fatal(err)
	}
}

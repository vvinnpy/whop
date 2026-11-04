package sshconn

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whop/internal/sshconfig"
)

func TestTerminalKeyboardInteractiveEchoed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	var output bytes.Buffer
	provider := TerminalPasswordProvider{In: input, Out: &output}
	answers, err := provider.KeyboardInteractive(sshconfig.Host{Alias: "test"}, "", "", []string{"Username:"}, []bool{true})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || answers[0] != "alice" {
		t.Fatalf("unexpected answers: %#v", answers)
	}
	if !strings.Contains(output.String(), "Username:") {
		t.Fatalf("prompt missing: %q", output.String())
	}
}

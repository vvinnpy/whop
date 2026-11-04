//go:build !windows

package sshconn

import (
	"bytes"
	"testing"
	"time"

	"github.com/creack/pty"

	"whop/internal/sshconfig"
)

func TestTerminalPasswordProviderPTY(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = master.Write([]byte("secret\n"))
	}()

	var output bytes.Buffer
	provider := TerminalPasswordProvider{In: slave, Out: &output}
	password, err := provider.Password(sshconfig.Host{Alias: "pty-host"})
	if err != nil {
		t.Fatal(err)
	}
	if password != "secret" {
		t.Fatalf("password = %q", password)
	}
	if !bytes.Contains(output.Bytes(), []byte("pty-host password:")) {
		t.Fatalf("prompt missing: %q", output.String())
	}
}

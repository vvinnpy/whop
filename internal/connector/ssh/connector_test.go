package sshconn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"

	"ttydeck/internal/sshconfig"
)

type testSSHServer struct {
	listener net.Listener
	server   *gliderssh.Server
	hostKey  cryptossh.Signer
}

func newTestSSHServer(t *testing.T, allowed cryptossh.PublicKey) *testSSHServer {
	t.Helper()

	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := cryptossh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}

	server := &gliderssh.Server{
		Handler: func(session gliderssh.Session) {
			command := session.Command()
			if len(command) > 0 {
				_, _ = fmt.Fprintf(session, "ran: %s\n", strings.Join(command, " "))
				_ = session.Exit(0)
				return
			}
			_, _ = io.WriteString(session, "hello from test\n")
			_ = session.Exit(0)
		},
		PublicKeyHandler: func(_ gliderssh.Context, key gliderssh.PublicKey) bool {
			if allowed == nil {
				return true
			}
			return bytes.Equal(key.Marshal(), allowed.Marshal())
		},
	}
	server.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ts := &testSSHServer{listener: listener, server: server, hostKey: hostSigner}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })
	return ts
}

func (s *testSSHServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func writeClientKey(t *testing.T) (string, cryptossh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := cryptossh.MarshalPrivateKey(private, "ttydeck test key")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, signer.PublicKey()
}

func newTestConnector() *Connector {
	return New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
	})
}

func TestRunCommandWithKeyAuth(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	connector := newTestConnector()

	host := sshconfig.Host{
		Alias:         "test-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}

	output, err := connector.RunCommand(context.Background(), host, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo hello") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunInteractiveShell(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	connector := newTestConnector()
	runner := NewRunner(connector, func() (int, int) { return 80, 24 })

	host := sshconfig.Host{
		Alias:         "test-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}

	var stdout, stderr bytes.Buffer
	if err := runner.Run(context.Background(), host, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "hello from test") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestNoAuthMethods(t *testing.T) {
	connector := newTestConnector()
	host := sshconfig.Host{
		Alias:         "test",
		HostName:      "127.0.0.1",
		Port:          22,
		IdentityFiles: []string{filepath.Join(t.TempDir(), "missing")},
	}
	_, err := connector.Dial(context.Background(), host)
	if err == nil || !strings.Contains(err.Error(), ErrNoAuth.Error()) {
		t.Fatalf("expected ErrNoAuth, got %v", err)
	}
}

func TestProxyJumpNotImplemented(t *testing.T) {
	connector := newTestConnector()
	host := sshconfig.Host{
		Alias:     "test",
		HostName:  "127.0.0.1",
		Port:      22,
		ProxyJump: "bastion",
	}
	_, err := connector.Dial(context.Background(), host)
	if err == nil || !strings.Contains(err.Error(), ErrProxyJump.Error()) {
		t.Fatalf("expected ErrProxyJump, got %v", err)
	}
}

func TestEncryptedKeyRequiresPassphraseProvider(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := cryptossh.MarshalPrivateKeyWithPassphrase(private, "encrypted", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	connector := newTestConnector()
	host := sshconfig.Host{
		Alias:         "test",
		HostName:      "127.0.0.1",
		Port:          22,
		IdentityFiles: []string{path},
	}
	_, err = connector.Dial(context.Background(), host)
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("expected passphrase error, got %v", err)
	}
}

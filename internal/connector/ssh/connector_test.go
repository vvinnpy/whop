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
	return newTestSSHServerWithAuth(t, allowed, "", nil)
}

func newTestSSHServerWithPassword(t *testing.T, password string) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, nil, password, nil)
}

func newTestSSHServerWithKeyboardInteractive(t *testing.T, password string) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, nil, "", func(_ gliderssh.Context, challenger cryptossh.KeyboardInteractiveChallenge) bool {
		answers, err := challenger("Password:", "", []string{"Password:"}, []bool{false})
		return err == nil && len(answers) == 1 && answers[0] == password
	})
}

func newTestSSHServerWithAuth(t *testing.T, allowed cryptossh.PublicKey, password string, keyboard gliderssh.KeyboardInteractiveHandler) *testSSHServer {
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
				return false
			}
			return bytes.Equal(key.Marshal(), allowed.Marshal())
		},
	}
	if password != "" {
		server.PasswordHandler = func(_ gliderssh.Context, candidate string) bool {
			return candidate == password
		}
	}
	if keyboard != nil {
		server.KeyboardInteractiveHandler = keyboard
	}
	server.AddHostKey(hostSigner)
	server.ChannelHandlers = map[string]gliderssh.ChannelHandler{
		"session":      gliderssh.DefaultChannelHandlers["session"],
		"direct-tcpip": gliderssh.DirectTCPIPHandler,
	}
	server.LocalPortForwardingCallback = func(gliderssh.Context, string, uint32) bool { return true }

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

func TestMultiHopProxyJumpNotImplemented(t *testing.T) {
	connector := newTestConnector()
	host := sshconfig.Host{
		Alias:     "test",
		HostName:  "127.0.0.1",
		Port:      22,
		ProxyJump: "bastion,inner",
	}
	_, err := connector.Dial(context.Background(), host)
	if err == nil || !strings.Contains(err.Error(), ErrProxyJump.Error()) {
		t.Fatalf("expected ErrProxyJump, got %v", err)
	}
}

func TestProxyJump(t *testing.T) {
	keyPath, public := writeClientKey(t)
	target := newTestSSHServer(t, public)
	jump := newTestSSHServer(t, public)

	jumpHost := sshconfig.Host{
		Alias:         "jump",
		HostName:      "127.0.0.1",
		Port:          jump.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	targetHost := sshconfig.Host{
		Alias:         "target",
		HostName:      "127.0.0.1",
		Port:          target.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
		ProxyJump:     "jump",
	}

	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		ResolveHost: func(alias string) (sshconfig.Host, bool) {
			if alias == "jump" {
				return jumpHost, true
			}
			return sshconfig.Host{}, false
		},
	})

	output, err := connector.RunCommand(context.Background(), targetHost, "echo via-jump")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo via-jump") {
		t.Fatalf("unexpected output: %q", output)
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

type fakePassphraseProvider struct {
	passphrase string
	err        error
}

func (p fakePassphraseProvider) Passphrase(sshconfig.Host, string) (string, error) {
	return p.passphrase, p.err
}

func writeEncryptedClientKey(t *testing.T, passphrase string) (string, cryptossh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := cryptossh.MarshalPrivateKeyWithPassphrase(private, "encrypted ttydeck key", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, signer.PublicKey()
}

func TestEncryptedKeyWithPassphraseProvider(t *testing.T) {
	keyPath, public := writeEncryptedClientKey(t, "correct horse")
	server := newTestSSHServer(t, public)
	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		Passphrase:      fakePassphraseProvider{passphrase: "correct horse"},
	})
	host := sshconfig.Host{
		Alias:         "encrypted",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	output, err := connector.RunCommand(context.Background(), host, "echo encrypted")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo encrypted") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestParseJumpSpec(t *testing.T) {
	tests := []struct {
		spec  string
		alias string
		user  string
		port  int
	}{
		{"bastion", "bastion", "", 0},
		{"deploy@bastion", "bastion", "deploy", 0},
		{"deploy@bastion:2222", "bastion", "deploy", 2222},
		{"[2001:db8::1]:2200", "2001:db8::1", "", 2200},
		{"ops@[2001:db8::1]:22", "2001:db8::1", "ops", 22},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			alias, user, port := parseJumpSpec(tt.spec)
			if alias != tt.alias || user != tt.user || port != tt.port {
				t.Fatalf("got %q %q %d, want %q %q %d", alias, user, port, tt.alias, tt.user, tt.port)
			}
		})
	}
}

func TestResolveJumpHostFallbackAndOverride(t *testing.T) {
	connector := New(Options{})
	host, err := connector.resolveJumpHost("bastion", "deploy", 2222)
	if err != nil {
		t.Fatal(err)
	}
	if host.Alias != "bastion" || host.HostName != "bastion" || host.User != "deploy" || host.Port != 2222 {
		t.Fatalf("unexpected fallback host: %+v", host)
	}

	connector = New(Options{ResolveHost: func(alias string) (sshconfig.Host, bool) {
		return sshconfig.Host{Alias: alias, HostName: "10.0.0.1", User: "from-config", Port: 22}, alias == "bastion"
	}})
	host, err = connector.resolveJumpHost("bastion", "override", 2200)
	if err != nil {
		t.Fatal(err)
	}
	if host.HostName != "10.0.0.1" || host.User != "override" || host.Port != 2200 {
		t.Fatalf("unexpected resolved host: %+v", host)
	}
}

func TestExecCommandSetters(t *testing.T) {
	cmd := &ExecCommand{}
	stdin := strings.NewReader("in")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetStdin(stdin)
	cmd.SetStdout(stdout)
	cmd.SetStderr(stderr)
	if cmd.Stdin != stdin || cmd.Stdout != stdout || cmd.Stderr != stderr {
		t.Fatalf("unexpected command fields: %+v", cmd)
	}
}

type fakePasswordProvider struct {
	password string
	calls    int
}

func (p *fakePasswordProvider) Password(sshconfig.Host) (string, error) {
	p.calls++
	return p.password, nil
}

type fakeKeyboardInteractiveProvider struct {
	password string
	calls    int
}

func (p *fakeKeyboardInteractiveProvider) Password(sshconfig.Host) (string, error) {
	return "unused", nil
}

func (p *fakeKeyboardInteractiveProvider) KeyboardInteractive(_ sshconfig.Host, _, _ string, questions []string, _ []bool) ([]string, error) {
	p.calls++
	answers := make([]string, len(questions))
	for i := range answers {
		answers[i] = p.password
	}
	return answers, nil
}

func TestPasswordAuth(t *testing.T) {
	server := newTestSSHServerWithPassword(t, "secret")
	provider := &fakePasswordProvider{password: "secret"}
	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		Password:        provider,
	})
	host := sshconfig.Host{
		Alias:         "password-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{filepath.Join(t.TempDir(), "missing")},
	}

	output, err := connector.RunCommand(context.Background(), host, "echo password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo password") {
		t.Fatalf("unexpected output: %q", output)
	}
	if provider.calls == 0 {
		t.Fatal("password provider was not called")
	}
}

func TestKeyboardInteractiveAuth(t *testing.T) {
	server := newTestSSHServerWithKeyboardInteractive(t, "otp-secret")
	provider := &fakeKeyboardInteractiveProvider{password: "otp-secret"}
	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		Password:        provider,
	})
	host := sshconfig.Host{
		Alias:         "kbi-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{filepath.Join(t.TempDir(), "missing")},
	}

	output, err := connector.RunCommand(context.Background(), host, "echo kbi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo kbi") {
		t.Fatalf("unexpected output: %q", output)
	}
	if provider.calls == 0 {
		t.Fatal("keyboard-interactive provider was not called")
	}
}

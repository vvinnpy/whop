package sshconn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"

	"whop/internal/session"
	"whop/internal/sshconfig"
)

type testSSHServer struct {
	listener net.Listener
	server   *gliderssh.Server
	hostKey  cryptossh.Signer

	mu        sync.Mutex
	forwarded []string
}

func newTestSSHServer(t *testing.T, allowed cryptossh.PublicKey) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, allowed, "", nil, true)
}

func newTestSSHServerWithPassword(t *testing.T, password string) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, nil, password, nil, true)
}

func newTestSSHServerWithKeyboardInteractive(t *testing.T, password string) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, nil, "", func(_ gliderssh.Context, challenger cryptossh.KeyboardInteractiveChallenge) bool {
		answers, err := challenger("Password:", "", []string{"Password:"}, []bool{false})
		return err == nil && len(answers) == 1 && answers[0] == password
	}, true)
}

func newTestSSHServerWithNoPTY(t *testing.T, allowed cryptossh.PublicKey) *testSSHServer {
	t.Helper()
	return newTestSSHServerWithAuth(t, allowed, "", nil, false)
}

func newTestSSHServerWithAuth(t *testing.T, allowed cryptossh.PublicKey, password string, keyboard gliderssh.KeyboardInteractiveHandler, allowPTY bool) *testSSHServer {
	t.Helper()

	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := cryptossh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}

	ts := &testSSHServer{}
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
	server.PtyCallback = func(gliderssh.Context, gliderssh.Pty) bool { return allowPTY }
	server.AddHostKey(hostSigner)
	server.ChannelHandlers = map[string]gliderssh.ChannelHandler{
		"session":      gliderssh.DefaultChannelHandlers["session"],
		"direct-tcpip": gliderssh.DirectTCPIPHandler,
	}
	server.LocalPortForwardingCallback = func(_ gliderssh.Context, destination string, port uint32) bool {
		ts.mu.Lock()
		ts.forwarded = append(ts.forwarded, fmt.Sprintf("%s:%d", destination, port))
		ts.mu.Unlock()
		return true
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ts.listener = listener
	ts.server = server
	ts.hostKey = hostSigner
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })
	return ts
}

func (s *testSSHServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func (s *testSSHServer) address() string {
	return s.listener.Addr().String()
}

func (s *testSSHServer) forwardedDestinations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.forwarded...)
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
	block, err := cryptossh.MarshalPrivateKey(private, "whop test key")
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

func TestMultiHopProxyJump(t *testing.T) {
	keyPath, public := writeClientKey(t)
	target := newTestSSHServer(t, public)
	inner := newTestSSHServer(t, public)
	outer := newTestSSHServer(t, public)

	configPath := filepath.Join(t.TempDir(), "config")
	config := fmt.Sprintf("Host inner\n  HostName 127.0.0.1\n  Port %d\n  User tester\n  IdentityFile %s\n", inner.port(), keyPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	outerHost := sshconfig.Host{
		Alias:         "outer",
		HostName:      "127.0.0.1",
		Port:          outer.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	targetHost := sshconfig.Host{
		Alias:         "target",
		HostName:      "127.0.0.1",
		Port:          target.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
		ProxyJump:     "inner,outer",
	}

	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		ConfigPath:      configPath,
		ResolveHost: func(alias string) (sshconfig.Host, bool) {
			if alias == "outer" {
				return outerHost, true
			}
			return sshconfig.Host{}, false
		},
	})

	output, err := connector.RunCommand(context.Background(), targetHost, "echo via-two-jumps")
	if err != nil {
		t.Fatalf("multi-hop command: %v", err)
	}
	if !strings.Contains(string(output), "ran: echo via-two-jumps") {
		t.Fatalf("unexpected output: %q", output)
	}
	if !containsString(outer.forwardedDestinations(), inner.address()) {
		t.Fatalf("outer jump forwarded %v, want %s", outer.forwardedDestinations(), inner.address())
	}
	if !containsString(inner.forwardedDestinations(), target.address()) {
		t.Fatalf("inner jump forwarded %v, want %s", inner.forwardedDestinations(), target.address())
	}
	if containsString(outer.forwardedDestinations(), target.address()) {
		t.Fatalf("outer jump bypassed inner jump: %v", outer.forwardedDestinations())
	}
}

func TestProxyJumpRejectsInvalidChains(t *testing.T) {
	tests := []struct {
		name      string
		proxyJump string
		want      string
	}{
		{name: "target self loop", proxyJump: "target,outer", want: "itself"},
		{name: "duplicate hop", proxyJump: "outer,outer", want: "repeats"},
		{name: "empty middle", proxyJump: "outer,,inner", want: "empty hop"},
		{name: "empty first", proxyJump: ",outer", want: "empty hop"},
		{name: "empty last", proxyJump: "outer,", want: "empty hop"},
		{name: "only separator", proxyJump: ",", want: "empty hop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connector := newTestConnector()
			host := sshconfig.Host{
				Alias:     "target",
				HostName:  "127.0.0.1",
				Port:      22,
				ProxyJump: tt.proxyJump,
			}
			_, err := connector.Dial(context.Background(), host)
			if err == nil {
				t.Fatal("expected ProxyJump validation error")
			}
			var stageErr *StageError
			if !errors.As(err, &stageErr) || stageErr.Stage != "jump" {
				t.Fatalf("expected stage jump, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
			if errors.Is(err, ErrProxyJump) {
				t.Fatalf("multi-hop validation returned ErrProxyJump: %v", err)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	block, err := cryptossh.MarshalPrivateKeyWithPassphrase(private, "encrypted whop key", []byte(passphrase))
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

func TestInteractiveSessionRecording(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	connector := newTestConnector()
	var cast, transcript bytes.Buffer
	runner := NewRunner(connector, func() (int, int) { return 80, 24 })
	runner.RecordingFactory = func(_ sshconfig.Host, width, height int) (io.WriteCloser, error) {
		return session.NewRecording(&cast, &transcript, width, height, time.Now())
	}

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
	if !strings.Contains(transcript.String(), "hello from test") {
		t.Fatalf("transcript = %q", transcript.String())
	}
	if !strings.Contains(cast.String(), `"o"`) || !strings.Contains(cast.String(), "hello from test") {
		t.Fatalf("cast = %q", cast.String())
	}
}

func TestInteractiveSessionFallsBackWhenPTYRejected(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServerWithNoPTY(t, public)
	connector := newTestConnector()
	runner := NewRunner(connector, func() (int, int) { return 80, 24 })
	host := sshconfig.Host{
		Alias:         "no-pty",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	var stdout, stderr bytes.Buffer
	if err := runner.Run(context.Background(), host, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "refused PTY") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello from test") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDefaultHostKeyAlgorithmsPreferED25519(t *testing.T) {
	algorithms := defaultHostKeyAlgorithms()
	if len(algorithms) < 3 {
		t.Fatalf("algorithms = %#v", algorithms)
	}
	if algorithms[0] != cryptossh.CertAlgoED25519v01 || algorithms[6] != cryptossh.KeyAlgoED25519 {
		t.Fatalf("unexpected preference order: %#v", algorithms)
	}
	foundLegacyRSA := false
	for _, algorithm := range algorithms {
		if algorithm == cryptossh.KeyAlgoRSA {
			foundLegacyRSA = true
		}
	}
	if !foundLegacyRSA {
		t.Fatalf("legacy RSA compatibility missing: %#v", algorithms)
	}
}

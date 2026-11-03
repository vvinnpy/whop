// Package sshconn provides an in-process SSH connector and interactive runner.
package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"ttydeck/internal/sshconfig"
)

var (
	// ErrNoAuth means no usable key or agent was found.
	ErrNoAuth = errors.New("no SSH authentication method available")
	// ErrProxyJump is returned for unsupported ProxyJump configurations.
	ErrProxyJump = errors.New("ProxyJump configuration is not supported")
	// ErrInteractiveUnavailable means the remote endpoint refused a PTY and
	// does not provide an interactive shell.
	ErrInteractiveUnavailable = errors.New("remote host does not provide an interactive shell")
)

// PassphraseProvider supplies a private-key passphrase when needed.
type PassphraseProvider interface {
	Passphrase(host sshconfig.Host, keyPath string) (string, error)
}

// PasswordProvider supplies an SSH login password when the server offers
// password authentication.
type PasswordProvider interface {
	Password(host sshconfig.Host) (string, error)
}

// KeyboardInteractiveProvider answers keyboard-interactive challenges.
type KeyboardInteractiveProvider interface {
	KeyboardInteractive(host sshconfig.Host, name, instruction string, questions []string, echos []bool) ([]string, error)
}

// Options configures a Connector.
type Options struct {
	HostKeyCallback ssh.HostKeyCallback
	Passphrase      PassphraseProvider
	Password        PasswordProvider
	AgentSocket     string
	DisableAgent    bool
	Timeout         time.Duration
	Username        string
	// ResolveHost resolves a jump-host alias to a configured host.
	ResolveHost func(alias string) (sshconfig.Host, bool)
	// ConfigPath is used as a fallback resolver for jump hosts.
	ConfigPath string
}

// Connector dials SSH hosts using x/crypto/ssh.
type Connector struct {
	opts Options
}

// New creates an SSH connector.
func New(opts Options) *Connector {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	return &Connector{opts: opts}
}

// Dial establishes an SSH client connection.
func (c *Connector) Dial(ctx context.Context, host sshconfig.Host) (*ssh.Client, error) {
	if host.ProxyJump != "" && !strings.EqualFold(host.ProxyJump, "none") {
		return c.dialViaJump(ctx, host)
	}
	return c.dialDirect(ctx, host)
}

func (c *Connector) dialDirect(ctx context.Context, host sshconfig.Host) (*ssh.Client, error) {
	cfg, cleanup, err := c.newClientConfig(host)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	address := host.Address()
	dialer := net.Dialer{Timeout: c.opts.Timeout}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	conn, chans, reqs, err := ssh.NewClientConn(netConn, address, cfg)
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", address, err)
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func (c *Connector) dialViaJump(ctx context.Context, host sshconfig.Host) (*ssh.Client, error) {
	specs := strings.Split(host.ProxyJump, ",")
	if len(specs) != 1 {
		return nil, fmt.Errorf("%w: multi-hop ProxyJump %q", ErrProxyJump, host.ProxyJump)
	}

	alias, user, port := parseJumpSpec(specs[0])
	if alias == "" {
		return nil, fmt.Errorf("ProxyJump %q has no host", host.ProxyJump)
	}
	if strings.EqualFold(alias, host.Alias) {
		return nil, fmt.Errorf("ProxyJump %q points to itself", host.ProxyJump)
	}

	jumpHost, err := c.resolveJumpHost(alias, user, port)
	if err != nil {
		return nil, err
	}
	jumpClient, err := c.Dial(ctx, jumpHost)
	if err != nil {
		return nil, fmt.Errorf("dial jump host %s: %w", jumpHost.Alias, err)
	}

	targetConn, err := jumpClient.Dial("tcp", host.Address())
	if err != nil {
		_ = jumpClient.Close()
		return nil, fmt.Errorf("open %s via %s: %w", host.Address(), jumpHost.Alias, err)
	}

	cfg, cleanup, err := c.newClientConfig(host)
	if err != nil {
		_ = targetConn.Close()
		_ = jumpClient.Close()
		return nil, err
	}
	defer cleanup()

	conn, chans, reqs, err := ssh.NewClientConn(targetConn, host.Address(), cfg)
	if err != nil {
		_ = targetConn.Close()
		_ = jumpClient.Close()
		return nil, fmt.Errorf("ssh handshake %s via %s: %w", host.Address(), jumpHost.Alias, err)
	}
	client := ssh.NewClient(conn, chans, reqs)
	go func() {
		_ = client.Wait()
		_ = jumpClient.Close()
	}()
	return client, nil
}

func (c *Connector) newClientConfig(host sshconfig.Host) (*ssh.ClientConfig, func(), error) {
	methods, cleanup, err := c.authMethods(host)
	if err != nil {
		return nil, func() {}, err
	}
	return &ssh.ClientConfig{
		User:            c.username(host),
		Auth:            methods,
		HostKeyCallback: c.hostKeyCallback(),
		Timeout:         c.opts.Timeout,
	}, cleanup, nil
}

func (c *Connector) resolveJumpHost(alias, user string, port int) (sshconfig.Host, error) {
	var host sshconfig.Host
	var ok bool

	if c.opts.ResolveHost != nil {
		host, ok = c.opts.ResolveHost(alias)
	}
	if !ok && c.opts.ConfigPath != "" {
		hosts, err := sshconfig.Load(c.opts.ConfigPath)
		if err != nil {
			return sshconfig.Host{}, fmt.Errorf("load jump host %s: %w", alias, err)
		}
		host, ok = sshconfig.Find(hosts, alias)
	}
	if !ok {
		host = sshconfig.Host{
			Alias:    alias,
			HostName: alias,
			Port:     22,
			Kind:     sshconfig.KindServer,
		}
	}
	if user != "" {
		host.User = user
	}
	if port > 0 {
		host.Port = port
	}
	return host, nil
}

func parseJumpSpec(spec string) (alias, user string, port int) {
	spec = strings.TrimSpace(spec)
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user = spec[:at]
		spec = spec[at+1:]
	}
	alias = spec
	if host, portText, err := net.SplitHostPort(spec); err == nil {
		alias = host
		if parsed, err := strconv.Atoi(portText); err == nil {
			port = parsed
		}
		return alias, user, port
	}
	if strings.Count(spec, ":") == 1 {
		if index := strings.LastIndex(spec, ":"); index > 0 {
			if parsed, err := strconv.Atoi(spec[index+1:]); err == nil {
				alias = spec[:index]
				port = parsed
			}
		}
	}
	return alias, user, port
}

// RunCommand runs a non-interactive command and returns combined output.
func (c *Connector) RunCommand(ctx context.Context, host sshconfig.Host, command string) ([]byte, error) {
	client, err := c.Dial(ctx, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", host.Alias, err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return output, fmt.Errorf("run command on %s: %w", host.Alias, err)
	}
	return output, nil
}

func (c *Connector) authMethods(host sshconfig.Host) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	var cleanups []func()
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}

	files := host.IdentityFiles
	if len(files) == 0 {
		files = defaultIdentityFiles()
	}
	for _, file := range files {
		path := expandHome(file)
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return nil, func() {}, fmt.Errorf("read identity %s: %w", path, err)
		}

		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			if c.opts.Passphrase == nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("parse identity %s: %w (encrypted keys require a passphrase provider)", path, err)
			}
			passphrase, passErr := c.opts.Passphrase.Passphrase(host, path)
			if passErr != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("passphrase for %s: %w", path, passErr)
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
		}
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("parse identity %s: %w", path, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if socket := c.agentSocket(); socket != "" && runtime.GOOS != "windows" {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			agentClient := agent.NewClient(conn)
			if keys, listErr := agentClient.List(); listErr == nil && len(keys) > 0 {
				methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
				cleanups = append(cleanups, func() { _ = conn.Close() })
			} else {
				_ = conn.Close()
			}
		}
	}

	if c.opts.Password != nil {
		methods = append(methods, ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
			return c.opts.Password.Password(host)
		}), 3))
	}
	if provider, ok := c.opts.Password.(KeyboardInteractiveProvider); ok {
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			return provider.KeyboardInteractive(host, name, instruction, questions, echos)
		}))
	}

	if len(methods) == 0 {
		return nil, func() {}, ErrNoAuth
	}
	return methods, cleanup, nil
}

func (c *Connector) agentSocket() string {
	if c.opts.DisableAgent {
		return ""
	}
	if c.opts.AgentSocket != "" {
		return expandHome(c.opts.AgentSocket)
	}
	return os.Getenv("SSH_AUTH_SOCK")
}

func (c *Connector) username(host sshconfig.Host) string {
	if host.User != "" {
		return host.User
	}
	if c.opts.Username != "" {
		return c.opts.Username
	}
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	if value := os.Getenv("USER"); value != "" {
		return value
	}
	return "root"
}

func (c *Connector) hostKeyCallback() ssh.HostKeyCallback {
	if c.opts.HostKeyCallback != nil {
		return c.opts.HostKeyCallback
	}
	return defaultHostKeyCallback()
}

func defaultIdentityFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_rsa"),
	}
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// RecordingFactory starts an output recorder for one session.
type RecordingFactory func(host sshconfig.Host, width, height int) (io.WriteCloser, error)

// Runner runs an interactive SSH shell.
type Runner struct {
	Connector        *Connector
	Size             func() (width, height int)
	RecordingFactory RecordingFactory
}

// NewRunner creates an interactive runner.
func NewRunner(connector *Connector, size func() (int, int)) *Runner {
	if size == nil {
		size = DefaultTerminalSize
	}
	return &Runner{Connector: connector, Size: size}
}

// Run opens an interactive shell and blocks until it exits.
func (r *Runner) Run(ctx context.Context, host sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer) error {
	client, err := r.Connector.Dial(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open session %s: %w", host.Alias, err)
	}
	defer session.Close()

	width, height := r.Size()
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	ptyRejected := false
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		if !isPTYRejected(err) {
			return fmt.Errorf("request pty on %s: %w", host.Alias, err)
		}
		ptyRejected = true
		if stderr != nil {
			fmt.Fprintf(stderr, "ttydeck: remote host refused PTY; continuing without a TTY\n")
		}
	}

	stdoutWriter := stdout
	if stdoutWriter == nil {
		stdoutWriter = io.Discard
	}
	if r.RecordingFactory != nil {
		recorder, err := r.RecordingFactory(host, width, height)
		if err != nil {
			return fmt.Errorf("start recording on %s: %w", host.Alias, err)
		}
		defer recorder.Close()
		stdoutWriter = io.MultiWriter(stdoutWriter, recorder)
	}

	session.Stdin = stdin
	session.Stdout = stdoutWriter
	session.Stderr = stderr
	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell on %s: %w", host.Alias, err)
	}
	if err := session.Wait(); err != nil {
		if ptyRejected {
			return fmt.Errorf("%w: %s", ErrInteractiveUnavailable, host.Alias)
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("remote shell on %s exited with status %d", host.Alias, exit.ExitStatus())
		}
		return fmt.Errorf("wait for shell on %s: %w", host.Alias, err)
	}
	return nil
}

func isPTYRejected(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "pty") &&
		(strings.Contains(message, "fail") || strings.Contains(message, "reject") || strings.Contains(message, "alloc"))
}

// ExecCommand adapts Runner to bubbletea.ExecCommand.
type ExecCommand struct {
	Runner *Runner
	Host   sshconfig.Host
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Run implements bubbletea.ExecCommand.
func (c *ExecCommand) Run() error {
	return c.Runner.Run(context.Background(), c.Host, c.Stdin, c.Stdout, c.Stderr)
}

// SetStdin implements bubbletea.ExecCommand.
func (c *ExecCommand) SetStdin(reader io.Reader) { c.Stdin = reader }

// SetStdout implements bubbletea.ExecCommand.
func (c *ExecCommand) SetStdout(writer io.Writer) { c.Stdout = writer }

// SetStderr implements bubbletea.ExecCommand.
func (c *ExecCommand) SetStderr(writer io.Writer) { c.Stderr = writer }

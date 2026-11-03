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
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"ttydeck/internal/sshconfig"
)

var (
	// ErrNoAuth means no usable key or agent was found.
	ErrNoAuth = errors.New("no SSH authentication method available")
	// ErrProxyJump is returned until ProxyJump is implemented.
	ErrProxyJump = errors.New("ProxyJump is not implemented in this milestone")
)

// PassphraseProvider supplies a private-key passphrase when needed.
type PassphraseProvider interface {
	Passphrase(host sshconfig.Host, keyPath string) (string, error)
}

// Options configures a Connector.
type Options struct {
	HostKeyCallback ssh.HostKeyCallback
	Passphrase      PassphraseProvider
	AgentSocket     string
	DisableAgent    bool
	Timeout         time.Duration
	Username        string
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
	if host.ProxyJump != "" {
		return nil, fmt.Errorf("%w: host %s uses ProxyJump %q", ErrProxyJump, host.Alias, host.ProxyJump)
	}

	methods, cleanup, err := c.authMethods(host)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cfg := &ssh.ClientConfig{
		User:            c.username(host),
		Auth:            methods,
		HostKeyCallback: c.hostKeyCallback(),
		Timeout:         c.opts.Timeout,
	}

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

	if socket := c.agentSocket(); socket != "" && runtime.GOOS != "windows" {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			agentClient := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
			cleanups = append(cleanups, func() { _ = conn.Close() })
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

func defaultHostKeyCallback() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		return func(string, net.Addr, ssh.PublicKey) error {
			return fmt.Errorf("resolve home directory for known_hosts: %w", err)
		}
	}
	path := filepath.Join(home, ".ssh", "known_hosts")
	callback, err := knownhosts.New(path)
	if err != nil {
		return func(string, net.Addr, ssh.PublicKey) error {
			return fmt.Errorf("load known_hosts %s: %w", path, err)
		}
	}
	return callback
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

// Runner runs an interactive SSH shell.
type Runner struct {
	Connector *Connector
	Size      func() (width, height int)
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
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("request pty on %s: %w", host.Alias, err)
	}

	session.Stdin = stdin
	session.Stdout = stdout
	session.Stderr = stderr
	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell on %s: %w", host.Alias, err)
	}
	if err := session.Wait(); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("remote shell on %s exited with status %d", host.Alias, exit.ExitStatus())
		}
		return fmt.Errorf("wait for shell on %s: %w", host.Alias, err)
	}
	return nil
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

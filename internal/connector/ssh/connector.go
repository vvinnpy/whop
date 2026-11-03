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
	// ErrProxyJump is retained for compatibility with older callers. ProxyJump
	// chains are supported; new code should inspect StageError instead.
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
	jump := strings.TrimSpace(host.ProxyJump)
	if jump != "" && !strings.EqualFold(jump, "none") {
		return c.dialViaJump(ctx, host)
	}
	return c.dialDirect(ctx, host)
}

func (c *Connector) dialDirect(ctx context.Context, host sshconfig.Host) (*ssh.Client, error) {
	cfg, cleanup, err := c.newClientConfig(host)
	if err != nil {
		return nil, &StageError{Stage: "auth", Host: host.Alias, Err: err}
	}
	defer cleanup()

	address := host.Address()
	dialer := net.Dialer{Timeout: c.opts.Timeout}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, &StageError{Stage: "dial", Host: host.Alias, Err: fmt.Errorf("dial %s: %w", address, err)}
	}

	conn, chans, reqs, err := ssh.NewClientConn(netConn, address, cfg)
	if err != nil {
		_ = netConn.Close()
		return nil, &StageError{Stage: "handshake", Host: host.Alias, Err: fmt.Errorf("ssh handshake %s: %w", address, err)}
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func (c *Connector) dialViaJump(ctx context.Context, host sshconfig.Host) (*ssh.Client, error) {
	hops, err := c.resolveJumpTargets(host)
	if err != nil {
		return nil, &StageError{Stage: "jump", Host: host.Alias, Err: err}
	}
	if len(hops) == 1 {
		return c.dialSingleJump(ctx, host, hops[0])
	}
	return c.dialMultiJump(ctx, host, hops)
}

type jumpTarget struct {
	host sshconfig.Host
}

func (c *Connector) dialSingleJump(ctx context.Context, host sshconfig.Host, jump jumpTarget) (*ssh.Client, error) {
	jumpClient, err := c.Dial(ctx, jump.host)
	if err != nil {
		return nil, &StageError{Stage: "jump", Host: host.Alias, Err: fmt.Errorf("dial jump host %s: %w", jump.host.Alias, err)}
	}

	client, err := c.dialTargetThrough(ctx, host, jumpClient, jump.host.Alias)
	if err != nil {
		_ = jumpClient.Close()
		return nil, err
	}
	closeJumpsWhenDone(client, []*ssh.Client{jumpClient})
	return client, nil
}

func (c *Connector) dialMultiJump(ctx context.Context, host sshconfig.Host, hops []jumpTarget) (*ssh.Client, error) {
	jumps := make([]*ssh.Client, 0, len(hops))
	closeJumps := func() { closeJumpClients(jumps) }

	// ProxyJump lists the hop closest to the target first. The local process
	// therefore connects to the last hop, which then connects toward the target.
	for i := len(hops) - 1; i >= 0; i-- {
		var via *ssh.Client
		if len(jumps) > 0 {
			via = jumps[len(jumps)-1]
		}
		client, err := c.dialJumpHop(ctx, hops[i].host, via)
		if err != nil {
			closeJumps()
			return nil, &StageError{Stage: "jump", Host: host.Alias, Err: fmt.Errorf("dial jump host %s: %w", hops[i].host.Alias, err)}
		}
		jumps = append(jumps, client)
	}

	client, err := c.dialTargetThrough(ctx, host, jumps[len(jumps)-1], hops[0].host.Alias)
	if err != nil {
		closeJumps()
		return nil, err
	}
	closeJumpsWhenDone(client, jumps)
	return client, nil
}

func (c *Connector) dialJumpHop(ctx context.Context, host sshconfig.Host, via *ssh.Client) (*ssh.Client, error) {
	cfg, cleanup, err := c.newClientConfig(host)
	if err != nil {
		return nil, fmt.Errorf("prepare authentication for %s: %w", host.Alias, err)
	}
	defer cleanup()

	address := host.Address()
	var netConn net.Conn
	if via == nil {
		dialer := net.Dialer{Timeout: c.opts.Timeout}
		netConn, err = dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", address, err)
		}
	} else {
		netConn, err = via.Dial("tcp", address)
		if err != nil {
			return nil, fmt.Errorf("open %s via %s: %w", address, via.RemoteAddr(), err)
		}
	}

	conn, chans, reqs, err := ssh.NewClientConn(netConn, address, cfg)
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", address, err)
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func (c *Connector) dialTargetThrough(ctx context.Context, host sshconfig.Host, jumpClient *ssh.Client, jumpName string) (*ssh.Client, error) {
	targetConn, err := jumpClient.Dial("tcp", host.Address())
	if err != nil {
		return nil, &StageError{Stage: "jump", Host: host.Alias, Err: fmt.Errorf("open %s via %s: %w", host.Address(), jumpName, err)}
	}

	cfg, cleanup, err := c.newClientConfig(host)
	if err != nil {
		_ = targetConn.Close()
		return nil, &StageError{Stage: "auth", Host: host.Alias, Err: err}
	}
	defer cleanup()

	conn, chans, reqs, err := ssh.NewClientConn(targetConn, host.Address(), cfg)
	if err != nil {
		_ = targetConn.Close()
		return nil, &StageError{Stage: "handshake", Host: host.Alias, Err: fmt.Errorf("ssh handshake %s via %s: %w", host.Address(), jumpName, err)}
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func closeJumpsWhenDone(client *ssh.Client, jumps []*ssh.Client) {
	go func() {
		_ = client.Wait()
		closeJumpClients(jumps)
	}()
}

func closeJumpClients(jumps []*ssh.Client) {
	for i := len(jumps) - 1; i >= 0; i-- {
		_ = jumps[i].Close()
	}
}

func (c *Connector) resolveJumpTargets(host sshconfig.Host) ([]jumpTarget, error) {
	specs := strings.Split(host.ProxyJump, ",")
	targets := make([]jumpTarget, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))

	targetNames := map[string]struct{}{}
	if alias := strings.TrimSpace(host.Alias); alias != "" {
		targetNames[strings.ToLower(alias)] = struct{}{}
	}
	if hostName := strings.TrimSpace(host.HostName); hostName != "" {
		targetNames[strings.ToLower(hostName)] = struct{}{}
	}

	for index, raw := range specs {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			return nil, fmt.Errorf("ProxyJump %q has an empty hop at position %d", host.ProxyJump, index+1)
		}
		alias, user, port := parseJumpSpec(spec)
		if alias == "" {
			return nil, fmt.Errorf("ProxyJump %q has no host at position %d", host.ProxyJump, index+1)
		}
		key := strings.ToLower(alias)
		if _, ok := targetNames[key]; ok {
			return nil, fmt.Errorf("ProxyJump %q points to itself via hop %q", host.ProxyJump, alias)
		}
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("ProxyJump %q repeats hop %q", host.ProxyJump, alias)
		}

		resolved, err := c.resolveJumpHost(alias, user, port)
		if err != nil {
			return nil, err
		}
		resolvedAlias := strings.ToLower(strings.TrimSpace(resolved.Alias))
		if resolvedAlias != "" && resolvedAlias != key {
			if _, ok := targetNames[resolvedAlias]; ok {
				return nil, fmt.Errorf("ProxyJump %q points to itself via hop %q", host.ProxyJump, resolved.Alias)
			}
			if _, ok := seen[resolvedAlias]; ok {
				return nil, fmt.Errorf("ProxyJump %q repeats hop %q", host.ProxyJump, resolved.Alias)
			}
		}

		seen[key] = struct{}{}
		if resolvedAlias != "" {
			seen[resolvedAlias] = struct{}{}
		}
		targets = append(targets, jumpTarget{host: resolved})
	}
	return targets, nil
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
		return nil, &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open session: %w", err)}
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return output, &StageError{Stage: "command", Host: host.Alias, Err: fmt.Errorf("run command: %w", err)}
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
		return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open session: %w", err)}
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
			return &StageError{Stage: "pty", Host: host.Alias, Err: fmt.Errorf("request pty: %w", err)}
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
			return &StageError{Stage: "record", Host: host.Alias, Err: fmt.Errorf("start recording: %w", err)}
		}
		defer recorder.Close()
		stdoutWriter = io.MultiWriter(stdoutWriter, recorder)
	}

	session.Stdin = stdin
	session.Stdout = stdoutWriter
	session.Stderr = stderr
	if err := session.Shell(); err != nil {
		return &StageError{Stage: "shell", Host: host.Alias, Err: fmt.Errorf("start shell: %w", err)}
	}
	if err := session.Wait(); err != nil {
		if ptyRejected {
			return &StageError{Stage: "pty", Host: host.Alias, Err: ErrInteractiveUnavailable}
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("remote shell exited with status %d", exit.ExitStatus())}
		}
		return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("wait for shell: %w", err)}
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

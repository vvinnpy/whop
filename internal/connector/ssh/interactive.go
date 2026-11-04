package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"ttydeck/internal/automation"
	"ttydeck/internal/sshconfig"
)

// RunInteractive opens a shell on host and gives its bidirectional terminal
// stream to interact. Output and remote stderr are copied through separate
// writers. The callback owns macro completion: when it returns, the SSH
// session is closed instead of waiting for an appliance shell to exit after
// stdin EOF. Context cancellation closes the session and unblocks stream I/O.
func (c *Connector) RunInteractive(ctx context.Context, host sshconfig.Host, stdout, stderr io.Writer, interact func(io.ReadWriter) error) error {
	if interact == nil {
		return errors.New("ssh interactive: nil callback")
	}
	client, err := c.Dial(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open session: %w", err)}
	}
	defer session.Close()

	width, height := DefaultTerminalSize()
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	// Automation often targets appliances that expose only a non-PTY shell.
	// Continue when the server rejects a PTY; expect matching still works on
	// the ordinary stdout stream.
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil && !isPTYRejected(err) {
		return &StageError{Stage: "pty", Host: host.Alias, Err: fmt.Errorf("request pty: %w", err)}
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open stdin: %w", err)}
	}
	remoteStdout, err := session.StdoutPipe()
	if err != nil {
		return &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open stdout: %w", err)}
	}

	var safeStdout, safeStderr io.Writer
	if stdout != nil {
		safeStdout = &lockedWriter{writer: stdout}
	}
	if stderr != nil {
		safeStderr = &lockedWriter{writer: stderr}
		session.Stderr = safeStderr
	}
	if err := session.Shell(); err != nil {
		return &StageError{Stage: "shell", Host: host.Alias, Err: fmt.Errorf("start shell: %w", err)}
	}

	reader := io.Reader(remoteStdout)
	if safeStdout != nil {
		reader = io.TeeReader(remoteStdout, safeStdout)
	}
	stream := &streamPair{Reader: reader, Writer: stdin, Closer: session}
	result := make(chan error, 1)
	go func() {
		result <- interact(stream)
	}()

	select {
	case interactErr := <-result:
		// The macro has completed (or failed). Its final state is authoritative;
		// do not turn appliance shells that ignore EOF into a macro failure.
		_ = session.Close()
		_ = session.Wait()
		if interactErr != nil {
			return &StageError{Stage: "automation", Host: host.Alias, Err: interactErr}
		}
		return nil
	case <-ctx.Done():
		_ = session.Close()
		<-result
		return &StageError{Stage: "session", Host: host.Alias, Err: ctx.Err()}
	}
}

type streamPair struct {
	io.Reader
	io.Writer
	io.Closer
}

// lockedWriter serializes stdout and stderr forwarding, which are delivered by
// separate SSH session goroutines.
type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

// MacroCommand adapts macro execution to bubbletea.ExecCommand.
type MacroCommand struct {
	Connector *Connector
	Host      sshconfig.Host
	Macro     automation.Macro
	OnEvent   automation.EventFunc
	Timeout   time.Duration
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// Run implements bubbletea.ExecCommand.
func (c *MacroCommand) Run() error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, timeout)
	defer cancel()
	err := c.Connector.RunInteractive(ctx, c.Host, c.Stdout, c.Stderr, func(stream io.ReadWriter) error {
		return automation.RunMacro(ctx, stream, c.Macro, automation.Options{CloseStream: true, OnEvent: c.OnEvent})
	})
	if errors.Is(err, context.Canceled) {
		return errors.New("macro canceled")
	}
	return err
}

// SetStdin implements bubbletea.ExecCommand.
func (c *MacroCommand) SetStdin(reader io.Reader) { c.Stdin = reader }

// SetStdout implements bubbletea.ExecCommand.
func (c *MacroCommand) SetStdout(writer io.Writer) { c.Stdout = writer }

// SetStderr implements bubbletea.ExecCommand.
func (c *MacroCommand) SetStderr(writer io.Writer) { c.Stderr = writer }

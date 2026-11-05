package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"whop/internal/sshconfig"
)

// PreparedSession is an authenticated interactive SSH session whose shell has
// already started. Output produced before Run is buffered so the caller can
// keep its own interface on screen while the connection is established without
// losing the remote banner or first prompt.
type PreparedSession struct {
	client   *ssh.Client
	session  *ssh.Session
	recorder io.WriteCloser

	stdin      io.WriteCloser
	stdoutPipe io.Reader

	stdoutSink *handoverWriter
	stderrSink *handoverWriter

	host        sshconfig.Host
	ptyRejected bool
	zmodem      *ZmodemOptions

	closeOnce sync.Once
	closeErr  error
	stdinOnce sync.Once
}

// Prepare dials host, authenticates, starts an interactive shell, and returns
// a session that can be handed to the local terminal later. It does not read
// from or change the local terminal.
func (r *Runner) Prepare(ctx context.Context, host sshconfig.Host) (*PreparedSession, error) {
	client, err := r.Connector.Dial(ctx, host)
	if err != nil {
		return nil, err
	}

	prepared := &PreparedSession{
		client: client,
		host:   host,
		zmodem: r.Zmodem,
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = prepared.Close()
		}
	}()

	session, err := client.NewSession()
	if err != nil {
		return nil, &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open session: %w", err)}
	}
	prepared.session = session

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
		if !isPTYRejected(err) {
			return nil, &StageError{Stage: "pty", Host: host.Alias, Err: fmt.Errorf("request pty: %w", err)}
		}
		prepared.ptyRejected = true
	}

	if r.RecordingFactory != nil {
		recorder, err := r.RecordingFactory(host, width, height)
		if err != nil {
			return nil, &StageError{Stage: "record", Host: host.Alias, Err: fmt.Errorf("start recording: %w", err)}
		}
		prepared.recorder = recorder
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return nil, &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open stdin: %w", err)}
	}
	prepared.stdin = stdinPipe

	prepared.stderrSink = newHandoverWriter(nil)
	session.Stderr = prepared.stderrSink
	if r.Zmodem != nil {
		stdoutPipe, err := session.StdoutPipe()
		if err != nil {
			return nil, &StageError{Stage: "session", Host: host.Alias, Err: fmt.Errorf("open stdout: %w", err)}
		}
		prepared.stdoutPipe = stdoutPipe
	} else {
		prepared.stdoutSink = newHandoverWriter(prepared.recorder)
		session.Stdout = prepared.stdoutSink
	}

	if err := session.Shell(); err != nil {
		return nil, &StageError{Stage: "shell", Host: host.Alias, Err: fmt.Errorf("start shell: %w", err)}
	}

	cleanup = false
	return prepared, nil
}

// Run hands a prepared session to the local terminal and blocks until the
// remote shell exits.
func (p *PreparedSession) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	if p == nil || p.session == nil {
		return errors.New("ssh: nil prepared session")
	}
	defer p.Close()

	if p.ptyRejected && stderr != nil {
		fmt.Fprintln(stderr, "whop: remote host refused PTY; continuing without a TTY")
	}

	rawStdin, terminal, err := prepareTerminalInput(stdin)
	if err != nil {
		return &StageError{Stage: "terminal", Host: p.host.Alias, Err: fmt.Errorf("enable local raw mode: %w", err)}
	}
	defer func() {
		if err := terminal.Restore(); err != nil && returnErr == nil {
			returnErr = &StageError{Stage: "terminal", Host: p.host.Alias, Err: fmt.Errorf("restore local terminal: %w", err)}
		}
	}()

	if p.stdoutPipe != nil {
		stdoutWriter := stdout
		if stdoutWriter == nil {
			stdoutWriter = io.Discard
		}
		if p.recorder != nil {
			stdoutWriter = io.MultiWriter(stdoutWriter, p.recorder)
		}
		if err := p.stderrSink.attach(stderr); err != nil {
			return &StageError{Stage: "terminal", Host: p.host.Alias, Err: fmt.Errorf("attach stderr: %w", err)}
		}

		remote := newRemoteStream(p.stdoutPipe, p.stdin, p.session.Close)
		defer func() { _ = remote.Close() }()
		stopCancel := context.AfterFunc(ctx, func() { _ = remote.Close() })
		defer stopCancel()
		proxyErr := runZmodemAwareShell(ctx, p.host, rawStdin, stdoutWriter, stderr, remote, terminal, p.zmodem)
		if proxyErr != nil {
			_ = remote.Close()
		}
		p.closeStdin()
		waitErr := p.session.Wait()
		if proxyErr != nil {
			return &StageError{Stage: "session", Host: p.host.Alias, Err: fmt.Errorf("interactive stream: %w", proxyErr)}
		}
		return shellWaitError(p.host.Alias, p.ptyRejected, waitErr)
	}

	if err := p.stdoutSink.attach(stdout); err != nil {
		return &StageError{Stage: "terminal", Host: p.host.Alias, Err: fmt.Errorf("attach stdout: %w", err)}
	}
	if err := p.stderrSink.attach(stderr); err != nil {
		return &StageError{Stage: "terminal", Host: p.host.Alias, Err: fmt.Errorf("attach stderr: %w", err)}
	}

	stopCancel := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stopCancel()
	go func() {
		_, _ = io.Copy(p.stdin, rawStdin)
		p.closeStdin()
	}()

	waitErr := p.session.Wait()
	p.closeStdin()
	return shellWaitError(p.host.Alias, p.ptyRejected, waitErr)
}

// Close releases the prepared session. It is safe to call more than once.
func (p *PreparedSession) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.closeStdin()
		if p.session != nil {
			p.closeErr = errors.Join(p.closeErr, p.session.Close())
		}
		if p.client != nil {
			p.closeErr = errors.Join(p.closeErr, p.client.Close())
		}
		if p.recorder != nil {
			p.closeErr = errors.Join(p.closeErr, p.recorder.Close())
		}
	})
	return p.closeErr
}

func (p *PreparedSession) closeStdin() {
	p.stdinOnce.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
	})
}

// handoverWriter buffers output until the terminal is ready, while optionally
// mirroring every byte to a recorder from the moment the shell starts.
type handoverWriter struct {
	mu      sync.Mutex
	pending bytes.Buffer
	mirror  io.Writer
	target  io.Writer
}

func newHandoverWriter(mirror io.Writer) *handoverWriter {
	return &handoverWriter{mirror: mirror}
}

func (w *handoverWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.mirror != nil {
		if _, err := w.mirror.Write(data); err != nil {
			return 0, err
		}
	}
	if w.target != nil {
		return w.target.Write(data)
	}
	if _, err := w.pending.Write(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *handoverWriter) attach(target io.Writer) error {
	if target == nil {
		target = io.Discard
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.target = target
	if w.pending.Len() == 0 {
		return nil
	}
	_, err := w.pending.WriteTo(target)
	return err
}

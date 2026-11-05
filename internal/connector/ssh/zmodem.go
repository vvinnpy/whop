package sshconn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"whop/internal/sshconfig"
)

const zmodemExitTimeout = 5 * time.Second

// RunZmodem starts command on host and hands its terminal stream to transfer.
// It is used for remote rz/sz and keeps the ZMODEM protocol in-process.
func (c *Connector) RunZmodem(ctx context.Context, host sshconfig.Host, command string, transfer func(io.ReadWriter) error) error {
	client, err := c.Dial(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("zmodem: open session %s: %w", host.Alias, err)
	}
	defer session.Close()

	width, height := DefaultTerminalSize()
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("zmodem: request pty on %s: %w", host.Alias, err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("zmodem: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("zmodem: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(command); err != nil {
		return fmt.Errorf("zmodem: start %q on %s: %w", command, host.Alias, err)
	}

	capture := newTailBuffer(4096)
	stream := newRemoteStream(io.TeeReader(stdout, capture), stdin, session.Close)
	defer func() { _ = stream.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopCancel()

	transferErr := transfer(stream)
	_ = stdin.Close()
	waitCh := make(chan error, 1)
	go func() { waitCh <- session.Wait() }()

	if transferErr != nil {
		if ctx.Err() != nil {
			transferErr = ctx.Err()
		}
		waitErr, timedOut := waitForZmodemExit(waitCh, 2*time.Second)
		if timedOut {
			_ = session.Close()
			waitErr = <-waitCh
		}
		remoteMessage := strings.TrimSpace(stderr.String())
		if output := printableTail(capture.String()); output != "" {
			if remoteMessage != "" {
				remoteMessage += " "
			}
			remoteMessage += output
		}
		return zmodemTransferError(transferErr, waitErr, remoteMessage)
	}

	waitErr, timedOut := waitForZmodemExit(waitCh, zmodemExitTimeout)
	if timedOut {
		// The transfer is complete; do not leave the CLI blocked on a sender
		// that failed to exit after the ZMODEM session.
		_ = session.Close()
		<-waitCh
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("zmodem: remote command on %s failed: %w (%s)", host.Alias, waitErr, message)
		}
		return fmt.Errorf("zmodem: remote command on %s failed: %w", host.Alias, waitErr)
	}
	return nil
}

func zmodemTransferError(transferErr, waitErr error, stderr string) error {
	message := strings.TrimSpace(stderr)
	switch {
	case message != "":
		return fmt.Errorf("zmodem: transfer failed: %w (remote: %s)", transferErr, message)
	case waitErr != nil:
		return fmt.Errorf("zmodem: transfer failed: %w (remote: %w)", transferErr, waitErr)
	default:
		return transferErr
	}
}

func waitForZmodemExit(waitCh <-chan error, timeout time.Duration) (error, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitCh:
		return err, false
	case <-timer.C:
		return nil, true
	}
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append(b.data[:0], b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func printableTail(data string) string {
	var out strings.Builder
	lastSpace := false
	for _, r := range data {
		switch r {
		case '\n', '\r', '\t':
			if out.Len() > 0 && !lastSpace {
				out.WriteByte(' ')
				lastSpace = true
			}
		default:
			if r < 0x20 || r >= 0x7f {
				continue
			}
			out.WriteRune(r)
			lastSpace = r == ' '
		}
	}
	return strings.TrimSpace(out.String())
}

// ShellQuote quotes one POSIX shell argument for the remote exec request.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

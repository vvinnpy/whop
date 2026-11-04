package sshconn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/ssh"

	"whop/internal/sshconfig"
)

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

	stream := &readWriteCloser{Reader: stdout, Writer: stdin}
	transferErr := transfer(stream)
	_ = stdin.Close()
	if transferErr != nil {
		_ = session.Close()
		return transferErr
	}
	if err := session.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("zmodem: remote command on %s failed: %w (%s)", host.Alias, err, message)
		}
		return fmt.Errorf("zmodem: remote command on %s failed: %w", host.Alias, err)
	}
	return nil
}

type readWriteCloser struct {
	io.Reader
	io.Writer
	io.Closer
}

// ShellQuote quotes one POSIX shell argument for the remote exec request.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

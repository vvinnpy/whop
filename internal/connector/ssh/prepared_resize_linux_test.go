package sshconn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	cryptossh "golang.org/x/crypto/ssh"

	"whop/internal/sshconfig"
)

var errTestUnauthorized = errors.New("test: unauthorized public key")

type testWindow struct {
	Width  int
	Height int
}

type testPTYRequest struct {
	Term         string
	Columns      uint32
	Rows         uint32
	Width        uint32
	Height       uint32
	TerminalMode string
}

type testWindowChange struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

func TestPreparedSessionForwardsTerminalWindowChanges(t *testing.T) {
	keyPath, public := writeClientKey(t)
	port, windows, serverErrs := startWindowTestSSHServer(t, public)

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	if err := pty.Setsize(master, &pty.Winsize{Rows: 31, Cols: 101}); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(newTestConnector(), func() (int, int) { return 80, 24 })
	host := sshconfig.Host{
		Alias:         "resize-host",
		HostName:      "127.0.0.1",
		Port:          port,
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	prepared, err := runner.Prepare(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })

	runDone := make(chan error, 1)
	go func() {
		runDone <- prepared.Run(context.Background(), slave, io.Discard, io.Discard)
	}()

	expectWindow(t, windows, testWindow{Width: 80, Height: 24})
	expectWindow(t, windows, testWindow{Width: 101, Height: 31})

	if err := pty.Setsize(master, &pty.Winsize{Rows: 45, Cols: 123}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	expectWindow(t, windows, testWindow{Width: 123, Height: 45})

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-serverErrs:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not exit after resize")
	}
}

func startWindowTestSSHServer(t *testing.T, public cryptossh.PublicKey) (int, <-chan testWindow, <-chan error) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}

	config := &cryptossh.ServerConfig{
		PublicKeyCallback: func(_ cryptossh.ConnMetadata, key cryptossh.PublicKey) (*cryptossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), public.Marshal()) {
				return nil, nil
			}
			return nil, errTestUnauthorized
		},
	}
	config.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	windows := make(chan testWindow, 8)
	serverErrs := make(chan error, 1)
	var reportOnce sync.Once
	report := func(err error) {
		if err == nil {
			return
		}
		reportOnce.Do(func() { serverErrs <- err })
	}

	go func() {
		connection, err := listener.Accept()
		if err != nil {
			report(err)
			return
		}
		defer connection.Close()
		serverConn, channels, requests, err := cryptossh.NewServerConn(connection, config)
		if err != nil {
			report(err)
			return
		}
		defer serverConn.Close()
		go cryptossh.DiscardRequests(requests)
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(cryptossh.UnknownChannelType, "session required")
				continue
			}
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				report(err)
				return
			}
			go serveWindowTestSession(channel, channelRequests, windows, report)
		}
	}()

	return listener.Addr().(*net.TCPAddr).Port, windows, serverErrs
}

func serveWindowTestSession(channel cryptossh.Channel, requests <-chan *cryptossh.Request, windows chan<- testWindow, report func(error)) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req":
			var payload testPTYRequest
			if err := cryptossh.Unmarshal(request.Payload, &payload); err != nil {
				report(err)
				_ = request.Reply(false, nil)
				continue
			}
			windows <- testWindow{Width: int(payload.Columns), Height: int(payload.Rows)}
			_ = request.Reply(true, nil)
		case "shell":
			_ = request.Reply(true, nil)
		case "env":
			_ = request.Reply(true, nil)
		case "window-change":
			var payload testWindowChange
			if err := cryptossh.Unmarshal(request.Payload, &payload); err != nil {
				report(err)
				_ = request.Reply(false, nil)
				continue
			}
			window := testWindow{Width: int(payload.Columns), Height: int(payload.Rows)}
			windows <- window
			_ = request.Reply(true, nil)
			if window == (testWindow{Width: 123, Height: 45}) {
				exitStatus := struct{ Status uint32 }{Status: 0}
				_, _ = channel.SendRequest("exit-status", false, cryptossh.Marshal(&exitStatus))
				return
			}
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func expectWindow(t *testing.T, windows <-chan testWindow, want testWindow) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case got := <-windows:
			if got == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for window %#v", want)
		}
	}
}

type terminalCaptureWriter struct {
	file *os.File
	buf  bytes.Buffer
}

func (w *terminalCaptureWriter) Write(data []byte) (int, error) {
	return w.buf.Write(data)
}

func (w *terminalCaptureWriter) UnwrapWriter() io.Writer {
	return w.file
}

func TestPreparedSessionResetsTerminalStyleBeforeRemoteOutput(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	localMaster, localSlave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer localMaster.Close()
	defer localSlave.Close()

	runner := NewRunner(newTestConnector(), func() (int, int) { return 80, 24 })
	host := sshconfig.Host{
		Alias:         "style-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	prepared, err := runner.Prepare(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })

	output := &terminalCaptureWriter{file: localSlave}
	if err := prepared.Run(context.Background(), localSlave, output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(output.buf.Bytes(), []byte(ansi.ResetStyle)) {
		t.Fatalf("remote output did not start with a terminal style reset: %q", output.buf.String())
	}
	if !bytes.Contains(output.buf.Bytes(), []byte("hello from test")) {
		t.Fatalf("remote output missing shell data: %q", output.buf.String())
	}
}

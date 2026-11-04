package sshconn

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"

	"whop/internal/automation"
	"whop/internal/sshconfig"
)

func TestRunInteractiveExecutesMacro(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newScriptedSSHServer(t, public)
	connector := newTestConnector()
	host := sshconfig.Host{
		Alias:         "scripted",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}

	var output bytes.Buffer
	macro := automation.Macro{
		Name: "login",
		Steps: []automation.Step{
			{Name: "login", Kind: automation.StepWait, Pattern: `login: `, Timeout: time.Second},
			{Name: "user", Kind: automation.StepSend, Send: []byte("admin\n")},
			{Name: "prompt", Kind: automation.StepWait, Pattern: `shell> `, Timeout: time.Second},
			{Name: "command", Kind: automation.StepSend, Send: []byte("uptime\n")},
		},
	}
	var events []automation.Event
	err := connector.RunInteractive(context.Background(), host, &output, nil, func(stream io.ReadWriter) error {
		return automation.RunMacro(context.Background(), stream, macro, automation.Options{
			OverallTimeout: 3 * time.Second,
			CloseStream:    true,
			OnEvent:        func(event automation.Event) { events = append(events, event) },
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "login: ") || !strings.Contains(output.String(), "shell> ") {
		t.Fatalf("output = %q", output.String())
	}
	foundCompleted := false
	for _, event := range events {
		if event.Type == automation.EventMacroCompleted {
			foundCompleted = true
		}
		if strings.Contains(event.Message, "admin") || strings.Contains(event.Message, "uptime") {
			t.Fatalf("audit event leaked send data: %#v", event)
		}
	}
	if !foundCompleted {
		t.Fatalf("missing completion event: %#v", events)
	}
}

func newScriptedSSHServer(t *testing.T, allowed cryptossh.PublicKey) *testSSHServer {
	t.Helper()
	return newScriptedSSHServerWithBehavior(t, allowed, scriptedServerBehavior{allowPTY: true})
}

type scriptedServerBehavior struct {
	allowPTY bool
	holdOpen bool
	errText  string
}

func newScriptedSSHServerWithBehavior(t *testing.T, allowed cryptossh.PublicKey, behavior scriptedServerBehavior) *testSSHServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	ts := &testSSHServer{}
	server := &gliderssh.Server{
		Handler: func(session gliderssh.Session) {
			reader := bufio.NewReader(session)
			_, _ = session.Write([]byte("login: "))
			user, _ := reader.ReadString('\n')
			if strings.TrimSpace(user) != "admin" {
				_ = session.Exit(2)
				return
			}
			_, _ = session.Write([]byte("shell> "))
			command, _ := reader.ReadString('\n')
			if behavior.errText != "" {
				_, _ = fmt.Fprintln(session.Stderr(), behavior.errText)
			}
			_, _ = session.Write([]byte("ran: " + strings.TrimSpace(command) + "\n"))
			if behavior.holdOpen {
				<-session.Context().Done()
				return
			}
			_ = session.Exit(0)
		},
		PublicKeyHandler: func(_ gliderssh.Context, key gliderssh.PublicKey) bool {
			return bytes.Equal(key.Marshal(), allowed.Marshal())
		},
		PtyCallback: func(gliderssh.Context, gliderssh.Pty) bool { return behavior.allowPTY },
	}
	server.AddHostKey(hostKey)
	server.ChannelHandlers = map[string]gliderssh.ChannelHandler{
		"session": gliderssh.DefaultChannelHandlers["session"],
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts.listener = listener
	ts.server = server
	ts.hostKey = hostKey
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return ts
}

func TestRunInteractiveDoesNotWaitForHeldOpenShell(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newScriptedSSHServerWithBehavior(t, public, scriptedServerBehavior{allowPTY: true, holdOpen: true})
	connector := newTestConnector()
	host := sshconfig.Host{Alias: "held-open", HostName: "127.0.0.1", Port: server.port(), User: "tester", IdentityFiles: []string{keyPath}}
	macro := automation.Macro{Name: "quick", Steps: []automation.Step{{Kind: automation.StepSend, Send: []byte("uptime\n")}}}

	start := time.Now()
	err := connector.RunInteractive(context.Background(), host, nil, nil, func(stream io.ReadWriter) error {
		return automation.RunMacro(context.Background(), stream, macro, automation.Options{CloseStream: true, OverallTimeout: time.Second})
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("held-open shell delayed completion: %s", elapsed)
	}
}

func TestRunInteractiveTimeoutReturns(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newScriptedSSHServerWithBehavior(t, public, scriptedServerBehavior{allowPTY: true, holdOpen: true})
	connector := newTestConnector()
	host := sshconfig.Host{Alias: "timeout", HostName: "127.0.0.1", Port: server.port(), User: "tester", IdentityFiles: []string{keyPath}}
	macro := automation.Macro{Name: "never", Steps: []automation.Step{{Kind: automation.StepWait, Pattern: "never", Timeout: 100 * time.Millisecond}}}

	done := make(chan error, 1)
	go func() {
		done <- connector.RunInteractive(context.Background(), host, nil, nil, func(stream io.ReadWriter) error {
			return automation.RunMacro(context.Background(), stream, macro, automation.Options{CloseStream: true, OverallTimeout: time.Second})
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, automation.ErrTimeout) {
			t.Fatalf("expected automation timeout, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("macro timeout did not return")
	}
}

func TestRunInteractiveSeparatesStderrWithoutPTY(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newScriptedSSHServerWithBehavior(t, public, scriptedServerBehavior{allowPTY: false, errText: "remote warning"})
	connector := newTestConnector()
	host := sshconfig.Host{Alias: "no-pty", HostName: "127.0.0.1", Port: server.port(), User: "tester", IdentityFiles: []string{keyPath}}
	macro := automation.Macro{Name: "login", Steps: []automation.Step{
		{Kind: automation.StepWait, Pattern: "login: ", Timeout: time.Second},
		{Kind: automation.StepSend, Send: []byte("admin\n")},
		{Kind: automation.StepWait, Pattern: "shell> ", Timeout: time.Second},
		{Kind: automation.StepSend, Send: []byte("uptime\n")},
		{Kind: automation.StepWait, Pattern: "ran: uptime", Timeout: time.Second},
	}}
	var stdout, stderr bytes.Buffer
	err := connector.RunInteractive(context.Background(), host, &stdout, &stderr, func(stream io.ReadWriter) error {
		return automation.RunMacro(context.Background(), stream, macro, automation.Options{CloseStream: true, OverallTimeout: 3 * time.Second})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "remote warning") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "remote warning") {
		t.Fatalf("stderr mixed into stdout: %q", stdout.String())
	}
}

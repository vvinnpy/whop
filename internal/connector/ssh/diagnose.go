package sshconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"ttydeck/internal/automation"
)

// StageError adds the failing SSH phase to an error.
type StageError struct {
	Stage string
	Host  string
	Err   error
}

func (e *StageError) Error() string {
	if e.Host != "" {
		return fmt.Sprintf("%s %s: %v", e.Stage, e.Host, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// Diagnostic is a machine-readable connection failure summary.
type Diagnostic struct {
	Stage    string `json:"stage"`
	Category string `json:"category"`
	Host     string `json:"host"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// Diagnose classifies an SSH connection error for humans and agents.
func Diagnose(host string, err error) Diagnostic {
	diagnostic := Diagnostic{Host: host, Message: errorString(err)}
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		diagnostic.Stage = stageErr.Stage
		if stageErr.Host != "" {
			diagnostic.Host = stageErr.Host
		}
	}
	if diagnostic.Stage == "" {
		diagnostic.Stage = inferStage(err)
	}

	inner := err
	if stageErr != nil {
		inner = stageErr.Err
	}
	diagnostic.Category = classifyCategory(inner)
	diagnostic.Hint = categoryHint(diagnostic.Category, diagnostic.Host)
	return diagnostic
}

func inferStage(err error) string {
	message := strings.ToLower(errorString(err))
	switch {
	case strings.Contains(message, "handshake"):
		return "handshake"
	case strings.Contains(message, "dial"):
		return "dial"
	case strings.Contains(message, "pty"):
		return "pty"
	case strings.Contains(message, "session"):
		return "session"
	default:
		return "unknown"
	}
}

func classifyCategory(err error) string {
	if errors.Is(err, ErrInteractiveUnavailable) {
		return "pty_rejected"
	}
	if errors.Is(err, automation.ErrTimeout) {
		return "automation_timeout"
	}
	if errors.Is(err, automation.ErrStreamClosed) {
		return "automation_stream_closed"
	}
	if errors.Is(err, automation.ErrBufferLimit) {
		return "automation_buffer_limit"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	message := strings.ToLower(errorString(err))
	switch {
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "no common algorithm"),
		strings.Contains(message, "no matching key exchange"),
		strings.Contains(message, "algorithm negotiation"):
		return "algorithm_mismatch"
	case strings.Contains(message, "knownhosts"),
		strings.Contains(message, "known_hosts"),
		strings.Contains(message, "host key"),
		strings.Contains(message, "key mismatch"):
		return "host_key"
	case strings.Contains(message, "unable to authenticate"),
		strings.Contains(message, "no supported methods"),
		strings.Contains(message, "permission denied"):
		return "authentication"
	case strings.Contains(message, "pty allocation"),
		strings.Contains(message, "pty-req"):
		return "pty_rejected"
	case message == "eof" || strings.Contains(message, " eof"),
		strings.HasSuffix(message, ": eof"),
		strings.Contains(message, "unexpected eof"):
		return "peer_closed"
	case strings.Contains(message, "connection reset"):
		return "connection_reset"
	default:
		return "unknown"
	}
}

func categoryHint(category, host string) string {
	switch category {
	case "peer_closed":
		hint := "TCP connected but the peer closed during the SSH handshake. Common causes: rate limiting/fail2ban, no common KEX or host-key algorithm, or a network middlebox."
		if strings.Contains(strings.ToLower(host), "aur.archlinux.org") {
			hint += " AUR rate-limits repeated connections; wait briefly and retry."
		}
		return hint
	case "timeout":
		return "The connection timed out. Check DNS, routing, firewall rules, and whether the SSH port is reachable."
	case "connection_refused":
		return "TCP was reachable but refused the connection. Check that sshd is listening on this address/port."
	case "dns":
		return "Hostname resolution failed. Check the HostName and DNS configuration."
	case "authentication":
		return "Authentication failed. Check IdentityFile, ssh-agent keys, authorized_keys, and server auth policy."
	case "host_key":
		return "Host-key verification failed. Check ~/.ssh/known_hosts; ttydeck filters unsupported algorithms but real key mismatches must be resolved manually."
	case "algorithm_mismatch":
		return "No common SSH algorithm. The server may be old/new, or the client may need explicit legacy algorithm support."
	case "pty_rejected":
		return "The remote endpoint does not provide an interactive shell. Use `ttydeck run <alias> <command>` instead."
	case "connection_reset":
		return "The connection was reset after TCP establishment. Check server logs, rate limits, and network middleboxes."
	case "automation_timeout":
		return "An expect step did not match in time. Check the pattern, device prompt, terminal mode, and per-step timeout."
	case "automation_stream_closed":
		return "The remote stream closed before the expect rule matched. Check whether the shell exited or the device rejected the session."
	case "automation_buffer_limit":
		return "Unmatched automation output exceeded the safety buffer. Make the expect patterns more specific or increase the buffer limit."
	case "canceled":
		return "The operation was canceled by the user or a parent context."
	default:
		return "Run with `ssh -vvv` against the same host to compare client and server negotiation."
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

package sshconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"whop/internal/automation"
)

func TestDiagnoseHandshakeEOF(t *testing.T) {
	diagnostic := Diagnose("aur.archlinux.org", fmt.Errorf("ssh: handshake failed: EOF"))
	if diagnostic.Stage != "handshake" || diagnostic.Category != "peer_closed" {
		t.Fatalf("unexpected diagnostic: %+v", diagnostic)
	}
	if !strings.Contains(strings.ToLower(diagnostic.Hint), "aur") {
		t.Fatalf("AUR hint missing: %q", diagnostic.Hint)
	}
}

func TestDiagnoseStages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		stage    string
		category string
	}{
		{"auth", &StageError{Stage: "auth", Host: "host", Err: errors.New("unable to authenticate")}, "auth", "authentication"},
		{"knownhosts", errors.New("knownhosts: key mismatch"), "unknown", "host_key"},
		{"refused", errors.New("dial tcp 1.2.3.4:22: connect: connection refused"), "dial", "connection_refused"},
		{"timeout", context.DeadlineExceeded, "unknown", "timeout"},
		{"pty", &StageError{Stage: "pty", Host: "host", Err: ErrInteractiveUnavailable}, "pty", "pty_rejected"},
		{"automation timeout", &StageError{Stage: "automation", Host: "host", Err: automation.ErrTimeout}, "automation", "automation_timeout"},
		{"automation stream", &StageError{Stage: "automation", Host: "host", Err: automation.ErrStreamClosed}, "automation", "automation_stream_closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostic := Diagnose("host", tt.err)
			if diagnostic.Stage != tt.stage || diagnostic.Category != tt.category {
				t.Fatalf("got %+v, want stage=%s category=%s", diagnostic, tt.stage, tt.category)
			}
			if diagnostic.Hint == "" {
				t.Fatal("hint must be present")
			}
		})
	}
}

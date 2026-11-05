// Package connector defines protocol-neutral connection capabilities.
package connector

import (
	"context"
	"io"

	"whop/internal/sshconfig"
)

// Diagnostic is a machine-readable connection failure summary.
type Diagnostic struct {
	Stage    string `json:"stage"`
	Category string `json:"category"`
	Host     string `json:"host"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// Connector exposes the operations the core service needs from a protocol
// adapter. Protocol-specific types must stay behind this interface.
type Connector interface {
	RunInteractive(ctx context.Context, host sshconfig.Host, stdout, stderr io.Writer, interact func(io.ReadWriter) error) error
	RunCommand(ctx context.Context, host sshconfig.Host, command string) ([]byte, error)
	RunZmodem(ctx context.Context, host sshconfig.Host, command string, transfer func(io.ReadWriter) error) error
	Forward(ctx context.Context, host sshconfig.Host, spec ForwardSpec, ready func(ForwardStatus) error) error
	OpenFileTransfer(ctx context.Context, host sshconfig.Host) (FileTransfer, error)
	Diagnose(host string, err error) Diagnostic
}

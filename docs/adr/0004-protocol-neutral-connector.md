# ADR 0004: Protocol-neutral connector capabilities

- Status: Accepted
- Date: 2026-11-05

## Context

The SSH connector originally exposed concrete SSH concepts directly to the CLI.
That was sufficient for the first release, but port forwarding, SFTP, and future
Serial/Telnet adapters would otherwise deepen protocol-specific branching in the
UI and command layer. The project contract requires protocol behavior behind a
`Connector` interface.

## Decision

Add `internal/connector.Connector` with protocol-neutral operations:

- interactive session;
- command execution;
- ZMODEM transfer;
- TCP forwarding;
- SFTP/file-transfer session creation;
- normalized diagnostics.

The SSH adapter implements this interface in `internal/connector/ssh`. Forwarding
rules use a shared `ForwardSpec` type for local, remote, and dynamic SOCKS5 modes.
SFTP file access uses a capability-scoped `FileTransfer` interface so UI code
does not import `pkg/sftp`.

`pkg/sftp` is the approved pure-Go SFTP implementation. Dynamic forwarding uses a
minimal in-process SOCKS5 CONNECT implementation with no authentication; its
default bind address is loopback.

## Consequences

Positive:

- The TUI and CLI work with protocol-neutral capabilities.
- Adding Serial or Telnet no longer requires changing UI file-browser logic.
- Forwarding and SFTP have in-process implementations with no external runtime
  binaries.

Negative:

- Connector implementations must report unsupported capabilities explicitly as
  the protocol surface grows.
- SFTP operations that do not accept a context can only check cancellation before
  and between copy operations; a hard transport stall still needs connection
  closure to unblock.
- Dynamic forwarding deliberately has no SOCKS authentication. Binding to a
  non-loopback address exposes an unauthenticated proxy.

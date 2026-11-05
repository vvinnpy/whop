# whop implementation plan

## Product direction

`whop` is a fast, local-first terminal connection deck. The interaction model follows
nccm's "type, filter, enter" speed, while the layout and information density follow tools
such as lazygit, k9s, and lazyssh.

The V1 product is SSH-first. Serial, Telnet, ZMODEM, and automation are built on the same
connector/session abstractions rather than bolted onto the UI.

## Milestones

### M0 — foundation (this iteration)

- SSH config discovery, include expansion, and host classification
- host model and nccm-style filter
- Bubble Tea host list, detail panel, filter bar, help overlay
- in-process SSH connector using `x/crypto/ssh`
- key and ssh-agent authentication
- CLI: `whop`, `whop list`, `whop connect`, `whop version`
- unit tests and an in-process SSH integration test
- three-platform CI with `CGO_ENABLED=0`

### M1 — safe configuration and metadata

- round-trip `~/.ssh/config` editing with raw-span preservation
- global lock, re-read, hash conflict detection, atomic replace, backups, diff preview
- tags, notes, favorites, recent connections
- SQLite metadata store
- password/keyboard-interactive prompts
- ProxyJump support
- dry-run-first nccm YAML migration with encrypted password vault import
- Argon2id + AES-256-GCM local host-password vault

### M2 — sessions and recording

- [x] interactive session runner with PTY handover
- [x] asciicast v2 writer
- [x] plain-text transcript search
- [x] built-in playback with `charmbracelet/x/vt`
- [x] SQLite FTS5 transcript index with stale-entry reconciliation
- [x] default plain-text transcript redaction
- [x] per-host recording policy

### M3 — automation

- [x] named command snippets
- [x] direct remote command execution and batch snippet execution
- [ ] snippet parameters
- [x] named macros with JSON definitions stored in SQLite
- [x] expect rules with timeouts and one-shot semantics
- [x] macro execution from CLI and TUI
- [ ] runbook engine
- [x] persisted automation audit log

### M4 — ZMODEM (`rz` / `sz`)

- [x] pure-Go ZMODEM engine (`github.com/xx25/go-zmodem`)
- [x] remote `rz` -> local upload, remote `sz` -> local download over SSH
- [x] manual CLI `zmodem put` / `zmodem get`
- [x] path traversal, overwrite, size, CRC, and cancellation safety
- [x] automatic ZMODEM header detection with interactive upload/download
- [ ] Serial transport integration
- [ ] Telnet binary-mode (IAC escaping) support

### M2.5 — SSH forwarding and files

- [x] local/remote/dynamic SOCKS5 forwarding with CLI and TUI
- [x] dual-pane SFTP browser with guarded upload/download
- [x] protocol-neutral Connector and FileTransfer interfaces

### M5 — additional connectors

- Serial connector via `go.bug.st/serial`
- Telnet connector with RFC 854 negotiation
- connector schema-driven forms
- optional FTP/WebDAV/S3 file browsers

### M6 — distribution

- [x] goreleaser for Linux/macOS/Windows amd64/arm64
- [x] release workflow and checksums
- [ ] AUR, deb/rpm, Homebrew, winget/scoop
- [ ] signing/notarization
- [ ] auto-update with signature verification

## M0 status

Completed in this iteration:

- [x] SSH config discovery, Include expansion, host classification
- [x] host model and nccm-style filter
- [x] Bubble Tea host list, detail panel, filter bar, help overlay
- [x] in-process SSH connector using `x/crypto/ssh`
- [x] key and ssh-agent authentication
- [x] password and keyboard-interactive prompts
- [x] single-hop and multi-hop ProxyJump
- [x] CLI: `whop`, `whop list`, `whop connect`, `whop version`
- [x] unit tests and an in-process SSH integration test
- [x] three-platform CI with `CGO_ENABLED=0`

No M0 items remain deferred.

## V1 status

The SSH-first V0.1 scope is functional:

- responsive nccm-style TUI
- SSH key/password/keyboard-interactive auth
- optional ssh-agent integration (disabled by default)
- single-hop and multi-hop ProxyJump
- local/remote/dynamic forwarding from CLI and TUI
- dual-pane SFTP browser from CLI and TUI
- structured connection diagnostics (`whop diagnose -json`)
- safe single-Host config editing with diff preview and stale-preview protection
- favorites, tags, notes, recents
- asciicast recording, replay, transcript search, per-host recording policy
- ZMODEM `rz`/`sz` over SSH
- snippets and batch command execution
- encrypted local host-password vault with automatic SSH password auth
- pure-Go/CGO-free cross-platform builds
- CI, race tests, goreleaser, and release workflow

Post-V0.1 work:

- runbook engine
- Serial/Telnet transports
- snippet parameters
- distro packaging, signing/notarization, and auto-update

## M0 architecture

```text
cmd/whop/          process entry point
internal/cli/         argument parsing and commands
internal/sshconfig/   config expansion, parsing, classification
internal/core/        host filtering and use cases
internal/connector/   Connector/FileTransfer interfaces and SSH implementation
internal/ui/          Bubble Tea model, views, keymaps
internal/platform/    OS-specific PTY/path helpers (introduced as needed)
```

## M0 acceptance criteria

- `whop` opens a responsive host list from the user's SSH config
- `/` explicitly focuses the filter; typing filters immediately while it is focused
- `enter` connects to the selected host using key or ssh-agent auth
- Git/Auth hosts and wildcard/Match entries are not shown as servers
- `whop list --json` returns machine-readable hosts
- all tests pass with `CGO_ENABLED=0`
- CI builds on Linux, macOS, and Windows

## Testing strategy

- unit tests for config expansion, classification, filtering, and TUI state
- golden test for the stable main-screen rendering
- fuzz test for SSH config parsing and Include handling
- in-process SSH server integration tests, including local/remote/dynamic forwarding and SFTP
- CLI tests with injected output writers
- race tests, `go vet`, `go mod tidy -diff`, and `CGO_ENABLED=0 go build ./...` in CI
- Linux/macOS/Windows test matrix plus cross-platform build matrix on every push

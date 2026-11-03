# ttydeck implementation plan

## Product direction

`ttydeck` is a fast, local-first terminal connection deck. The interaction model follows
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
- CLI: `ttydeck`, `ttydeck list`, `ttydeck connect`, `ttydeck version`
- unit tests and an in-process SSH integration test
- three-platform CI with `CGO_ENABLED=0`

### M1 — safe configuration and metadata

- round-trip `~/.ssh/config` editing with raw-span preservation
- global lock, re-read, hash conflict detection, atomic replace, backups, diff preview
- tags, notes, favorites, recent connections
- SQLite metadata store
- password/keyboard-interactive prompts
- ProxyJump support

### M2 — sessions and recording

- [x] interactive session runner with PTY handover
- [x] asciicast v2 writer
- [x] plain-text transcript search
- [x] built-in playback with `charmbracelet/x/vt`
- [ ] SQLite FTS5 transcript index
- [ ] per-host recording policy and redaction

### M3 — automation

- snippets with parameters
- macros
- expect rules with timeouts and one-shot semantics
- runbook engine
- audit log

### M4 — ZMODEM (`rz` / `sz`)

- pure-Go ZMODEM engine, pinned/vendored
- remote `rz` -> local upload, remote `sz` -> local download
- manual trigger first, automatic detection later
- SSH and serial transports
- Telnet binary-mode (IAC escaping) support after dedicated testing
- path traversal, overwrite, size, CRC, and cancellation safety

### M5 — additional connectors

- Serial connector via `go.bug.st/serial`
- Telnet connector with RFC 854 negotiation
- connector schema-driven forms
- optional SFTP/FTP/WebDAV/S3 file browsers

### M6 — distribution

- goreleaser for Linux/macOS/Windows amd64/arm64
- AUR, deb/rpm, Homebrew, winget/scoop
- checksums and signing
- auto-update with signature verification

## M0 status

Completed in this iteration:

- [x] SSH config discovery, Include expansion, host classification
- [x] host model and nccm-style filter
- [x] Bubble Tea host list, detail panel, filter bar, help overlay
- [x] in-process SSH connector using `x/crypto/ssh`
- [x] key and ssh-agent authentication
- [x] password and keyboard-interactive prompts
- [x] single-hop ProxyJump
- [x] CLI: `ttydeck`, `ttydeck list`, `ttydeck connect`, `ttydeck version`
- [x] unit tests and an in-process SSH integration test
- [x] three-platform CI with `CGO_ENABLED=0`

Deferred to the next milestone:

- [ ] password/keyboard-interactive prompts
- [ ] ProxyJump
- [ ] safe config writes and metadata

## M0 architecture

```text
cmd/ttydeck/          process entry point
internal/cli/         argument parsing and commands
internal/sshconfig/   config expansion, parsing, classification
internal/core/        host filtering and use cases
internal/connector/   connector interfaces and SSH implementation
internal/ui/          Bubble Tea model, views, keymaps
internal/platform/    OS-specific PTY/path helpers (introduced as needed)
```

## M0 acceptance criteria

- `ttydeck` opens a responsive host list from the user's SSH config
- typing filters immediately; `/` explicitly focuses the filter
- `enter` connects to the selected host using key or ssh-agent auth
- Git/Auth hosts and wildcard/Match entries are not shown as servers
- `ttydeck list --json` returns machine-readable hosts
- all tests pass with `CGO_ENABLED=0`
- CI builds on Linux, macOS, and Windows

## Testing strategy

- unit tests for config expansion, classification, filtering, and TUI state
- golden-ish view assertions for wide and narrow layouts
- in-process SSH server integration test; no system `sshd`
- CLI tests with injected output writers
- `go vet` and `CGO_ENABLED=0 go build ./...` in CI
- cross-platform build matrix on every push

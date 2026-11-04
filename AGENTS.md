# AGENTS.md — whop project contract

> 中文摘要：whop 是一个纯 Go、无 CGO、无外部运行时依赖的跨平台终端连接工作台。任何改动都不得破坏纯 Go、跨平台、本地优先、配置安全和密钥安全这五条底线。

This file is the project contract for humans and coding agents. Read it before changing code.

## 1. Product

`whop` is a fast, local-first terminal connection deck.

It manages terminal connections, sessions, and automation in one TUI, with a headless CLI and JSON output.
The first supported protocol is SSH; the architecture must allow Serial, Telnet, and other connectors later.

### V1 scope

- SSH via `golang.org/x/crypto/ssh`
- `~/.ssh/config` reading and safe editing
- known_hosts verification
- public-key, password, and keyboard-interactive auth
- local/remote/dynamic port forwarding
- SFTP dual-pane file transfer
- asciicast v2 session recording, playback, and transcript search
- ZMODEM `rz`/`sz` upload and download over SSH
- named snippets and batch remote command execution
- local SQLite metadata (favorites, tags, notes, recents)
- headless CLI with JSON output
- Linux, macOS, Windows

### Explicit non-goals for V1

- `ProxyCommand` and `Match exec` (they require external programs)
- PKCS#11, GSSAPI, X11 forwarding, FIDO2
- RDP and VNC
- cloud provider discovery
- Docker/Podman fleet management
- hosted sync / team accounts
- telemetry or analytics

## 2. Non-negotiable constraints

1. **Pure Go.** Project code must not use `import "C"`.
2. **No CGO.** Release builds must succeed with `CGO_ENABLED=0`.
3. **No required external binaries.** Core features must not call `ssh`, `scp`, `sftp`, `sshpass`,
   `telnet`, `picocom`, `tio`, `minicom`, `asciinema`, `tmux`, `xclip`, `xsel`, `git`, `openssl`,
   `gpg`, `xfreerdp`, or `vncviewer` at runtime.
4. **Cross-platform.** CI must build for Linux, macOS, and Windows (`amd64`; `arm64` where practical).
5. **Local-first.** No account, server, telemetry, or hidden network call is required to use the tool.
6. **Config safety.** Never silently rewrite unrelated `~/.ssh/config` content.
7. **Secret safety.** Never write passwords, passphrases, tokens, or private keys into plaintext config,
   SQLite, logs, or crash reports.
8. **Extensible core.** Protocol-specific behavior belongs behind a `Connector` interface.

If a proposed change conflicts with one of these constraints, stop and document the trade-off in an ADR
before implementing it.

## 3. Dependency policy

Prefer the Go standard library, then a small, maintained module.

A new runtime dependency is allowed only if all of the following are true:

- pure Go on Linux, macOS, and Windows (or has a pure-Go build path with `CGO_ENABLED=0`);
- no required external executable;
- permissively licensed (MIT, BSD, Apache-2.0, ISC);
- actively maintained or effectively complete;
- does not introduce a daemon, service, telemetry, or network account;
- passes cross-platform compile checks;
- is pinned in `go.mod`.

For a significant dependency or architectural choice, add an ADR under `docs/adr/`.

### Approved core dependencies

- `golang.org/x/crypto/ssh`
- `golang.org/x/crypto/ssh/knownhosts`
- `golang.org/x/crypto/ssh/agent`
- `github.com/pkg/sftp`
- `github.com/kevinburke/ssh_config`
- `go.bug.st/serial`
- `github.com/aymanbagabas/go-pty`
- `github.com/charmbracelet/x/vt`
- `github.com/charmbracelet/bubbletea`
- `github.com/charmbracelet/lipgloss`
- `github.com/charmbracelet/x/ansi`
- `github.com/gofrs/flock`
- `github.com/natefinch/atomic`
- `modernc.org/sqlite`
- `go.yaml.in/yaml/v3` — optional nccm YAML import; pure Go
- `filippo.io/age`
- `golang.org/x/crypto/argon2`
- `github.com/go-git/go-git/v5`
- `golang.design/x/clipboard`

### Test-only dependencies

- `github.com/gliderlabs/ssh` — in-process SSH server for integration tests; never imported by the runtime.
- `golang.org/x/term` — terminal size and password reads; pure Go.
- `github.com/charmbracelet/x/exp/teatest` — Bubble Tea event-loop tests; test-only.
- `github.com/creack/pty` — PTY-backed terminal prompt tests; test-only.

### Rejected dependencies

- `github.com/atotto/clipboard` — shells out to `xclip` / `xsel` / `pbcopy`.
- `github.com/99designs/keyring` — its macOS backend depends on CGO.
- `github.com/google/renameio/v2` — no Windows support.
- `github.com/tomatome/grdp` — CGO and not an importable library.
- `github.com/miekg/pkcs11` — CGO.
- `github.com/keys-pub/go-libfido2` — CGO.

## 4. Architecture

Planned layout:

```text
cmd/whop/          entry point
internal/ui/          Bubble Tea models, views, keymaps
internal/core/        use cases and orchestration
internal/connector/   Connector interface and implementations
internal/sshconfig/   parse, resolve, round-trip edit
internal/session/     PTY, recording, playback, macros
internal/store/       SQLite and migrations
internal/secrets/     age vault and optional OS keyring
internal/platform/    OS-specific PTY, locking, atomic replace, paths
internal/cli/         Cobra commands and JSON output
```

Rules:

- The TUI is a client of the core service layer; no business logic in views.
- Protocol adapters implement `Connector`; do not hard-code protocol branches in UI code.
- OS-specific behavior lives behind `internal/platform` and build tags.
- Prefer compile-time platform selection (`//go:build`) over runtime `runtime.GOOS` branching.
- All blocking I/O takes `context.Context`.
- No global mutable state. Dependencies are injected explicitly.

## 5. Configuration and SSH rules

`~/.ssh/config` is the source of truth for SSH connection parameters. Tool metadata (tags, notes,
history, macros, recording index) belongs in the local SQLite database.

Rules:

- Default to read-only. Edit only after an explicit user action.
- Never treat every `Host` block as a server.
- Exclude `Host *`, wildcard/negated patterns, `Match` blocks, and known Git/Auth hosts
  (`github.com`, `gitlab.com`, `bitbucket.org`, `codeberg.org`, `ssh.dev.azure.com`, `gitee.com`)
  from the server list by default.
- Never rewrite unrelated blocks. Preserve comments, blank lines, indentation, and Host order.
- Use raw-span editing rather than parse-then-serialize.
- Acquire a global lock, re-read the file, and compare a content hash before writing.
- Write to a temporary file in the same directory, fsync, and atomically replace the target.
- Preserve file mode (`0600`) and handle symlinked dotfile configs by writing the resolved target.
- Show a diff before saving. Keep timestamped backups. Roll back if validation fails.
- Validate the resulting config with our own resolver; do not depend on `ssh -G`.
- Never store secrets in `~/.ssh/config`.

## 6. Secrets and security

- Passwords, key passphrases, tokens, and private keys are never stored in plaintext.
- Default secret store: a local vault protected by a master password (Argon2id + AES-256-GCM).
- OS keyring is an optional enhancement, never a hard requirement.
- ssh-agent integration is optional and disabled by default. It must never be a required dependency.
- The macOS `/usr/bin/security` path may be used only as an optional integration, never as a requirement.
- Never pass secrets through command-line arguments or shell interpolation.
- Minimize secret lifetime in memory; do not log secrets or full environment dumps.
- Host key verification is on by default. Insecure mode is explicit per host and shows a warning.
- Do not add network calls, telemetry, or remote logging without explicit user approval.

## 7. Session recording

- Use asciicast v2 for recordings.
- Recording is opt-in per host and off by default.
- Record output only by default; input recording is a separate explicit option.
- Pause recording at password prompts and support configurable redaction rules.
- Store recordings locally by default; never upload automatically.
- Playback uses `github.com/charmbracelet/x/vt`; do not require the `asciinema` binary.
- Transcript search indexes plain text in SQLite FTS5; recordings remain the source of truth.

## 7.1 UX contract

- Follow `docs/ux.md`: type-to-filter, enter-to-connect, one-screen workflow.
- The TUI must be keyboard-first and usable without a mouse.
- Keep the common path (open, filter, connect) free of modal dialogs.
- Prefer a compact list plus progressive detail over a dashboard of panels.

## 8. Coding standards

- `gofmt` is mandatory.
- `go vet ./...` and `go test ./...` must pass.
- Use `go test -race ./...` for packages with concurrency.
- Wrap errors with context: `fmt.Errorf("connect %s: %w", host, err)`.
- No panics in library code; return errors.
- Prefer table-driven tests.
- Add golden tests for TUI rendering and fuzz tests for config parsing.
- Exported APIs and code comments in English; user-facing docs may be Chinese/English.
- Keep the binary startup path fast; never block startup on network probes.
- Keyboard-first UI; mouse is optional. Provide ASCII fallbacks for Nerd Font glyphs.

## 9. Testing and CI

Required local checks before committing:

```bash
gofmt -w .
go vet ./...
go test ./...
CGO_ENABLED=0 go build ./...
```

CI must also verify:

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
```

Integration tests should use in-process SSH servers, not a system `sshd`.
Windows-specific testing must cover ConPTY where available.

## 10. Git and release rules

- Main branch: `main`.
- Agent-created branches use the `codex/` prefix unless the user requests otherwise.
- Commit messages follow Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).
- Never push unless the user explicitly asks to push.
- Never force-push `main`.
- Releases use SemVer and `goreleaser`; artifacts include checksums.
- Do not add a remote, tag, or release without explicit instruction.

## 11. Definition of Done

A change is done only when:

- behavior is implemented and documented;
- tests cover the new path;
- `CGO_ENABLED=0` builds pass for Linux, macOS, and Windows;
- no new external runtime dependency was introduced;
- config/secret safety rules are preserved;
- a significant design decision is recorded in an ADR;
- `README.md` or relevant docs are updated.

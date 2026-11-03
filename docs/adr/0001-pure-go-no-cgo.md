# ADR 0001: Pure Go, no CGO, no required external binaries

- Status: Accepted
- Date: 2026-11-03

## Context

`ttydeck` must be easy to build, distribute, and maintain across Linux, macOS, and Windows.
Depending on system tools such as OpenSSH, telnet, picocom, or asciinema creates inconsistent
behavior and makes packaging fragile. CGO makes cross-compilation and static distribution harder.

## Decision

1. The runtime must be a single Go binary built with `CGO_ENABLED=0`.
2. No required external executable may be invoked for core functionality.
3. Optional integrations with OS services (for example a keyring) are allowed, but a pure-Go
   fallback must always exist.
4. Protocol-specific code is isolated behind a `Connector` interface.

## Consequences

Positive:

- One build matrix can produce Linux, macOS, and Windows binaries.
- No runtime installation of `ssh`, `telnet`, `picocom`, `telia`, or `asciinema`.
- Behavior is consistent across platforms.
- Easier static analysis, testing, and reproducible builds.

Negative:

- `ProxyCommand`, `Match exec`, `LocalCommand`, and `KnownHostsCommand` cannot be fully supported.
- PKCS#11, GSSAPI, X11 forwarding, and FIDO2 are deferred.
- RDP is not supported in V1; existing pure-Go RDP clients are not mature enough.
- The project owns more SSH/config/terminal behavior than a wrapper around OpenSSH.
- Legacy device compatibility must be tested explicitly.

## Allowed core dependencies

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
- `github.com/charmbracelet/bubbles`
- `github.com/charmbracelet/x/ansi`
- `github.com/gofrs/flock`
- `github.com/natefinch/atomic`
- `modernc.org/sqlite`
- `filippo.io/age`
- `golang.org/x/crypto/argon2`
- `github.com/go-git/go-git/v5`
- `golang.design/x/clipboard`

## Rejected dependencies

- `github.com/atotto/clipboard` — shells out to `xclip`/`xsel`/`pbcopy`.
- `github.com/99designs/keyring` — its macOS backend depends on CGO.
- `github.com/google/renameio/v2` — no Windows support.
- `github.com/tomatome/grdp` — CGO and not an importable library.
- `github.com/miekg/pkcs11` — CGO.
- `github.com/keys-pub/go-libfido2` — CGO.

## Verification

As of 2026-11-03, a smoke package importing the core dependency set builds with:

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
```

Runtime verification on Windows ConPTY, serial devices, and clipboard backends is still required.

# ttydeck

**A fast, local-first terminal connection deck.**

`ttydeck` is a cross-platform TUI for managing terminal connections, sessions, and automation.
It is designed to be a single pure-Go binary: no CGO, no external `ssh`/`telnet`/`picocom`/`asciinema`
runtime dependencies.

## Why ttydeck

Existing connection managers are good at one or two things, but rarely all of them:

- fast fuzzy launcher
- safe `~/.ssh/config` editing
- SSH, serial, and telnet in one place
- session recording and searchable transcripts
- macros and expect-style automation
- local-first storage, headless CLI, and machine-readable output

`ttydeck` is the attempt to combine those strengths without becoming an operations platform.

## Design principles

1. **Pure Go, no CGO** — `CGO_ENABLED=0` must build and run on Linux, macOS, and Windows.
2. **No required external binaries** — SSH, SFTP, serial, telnet, recording, and automation are in-process.
3. **Local-first** — configuration and history live on the user's machine; no account or server required.
4. **Fast by default** — open, search, connect. The common path should take one keystroke.
5. **Safe configuration** — never silently rewrite unrelated `~/.ssh/config` blocks.
6. **Composable** — every TUI action has a CLI/JSON equivalent where practical.
7. **Extensible** — protocol adapters are interfaces, not hard-coded assumptions.

## V1 scope

- SSH via `golang.org/x/crypto/ssh`
- Known-hosts verification and key-based authentication
- Password and keyboard-interactive authentication
- `~/.ssh/config` read/write with backup, diff, and conflict detection
- Fuzzy host search, tags, groups, favorites, and recent connections
- Local/remote/dynamic port forwarding
- SFTP dual-pane file browser
- asciicast v2 session recording and built-in playback
- Snippets, macros, and expect rules
- Local SQLite metadata, encrypted vault fallback
- Headless CLI with JSON output
- Linux, macOS, and Windows

## Not in V1

- `ProxyCommand` and `Match exec` (they require external programs)
- PKCS#11, GSSAPI, X11 forwarding, FIDO2
- RDP and VNC
- Cloud provider discovery
- Docker/Podman fleet management
- Team sync / hosted service

## Development

Read [AGENTS.md](AGENTS.md) before contributing. It defines the hard constraints for
pure-Go, cross-platform, config-safe development.

## Usage

```bash
# Interactive host list
ttydeck

# ssh-agent is optional and disabled by default
ttydeck -agent
TTYDECK_SSH_AGENT=1 ttydeck
ttydeck -no-agent

# Start with an initial filter
ttydeck prod

# Machine-readable host list
ttydeck list --json

# Connect from the CLI
ttydeck connect prod-db

# Local metadata
ttydeck favorite prod-db on
ttydeck tag prod-db prod db
ttydeck note prod-db "primary database"

# Record, replay, and search sessions
ttydeck -record
ttydeck connect -record prod-db
ttydeck replay ~/.config/ttydeck/recordings/prod-db-*.cast
ttydeck index                        # rebuild and prune the FTS5 index
ttydeck index --no-prune             # rebuild without removing stale entries
ttydeck search "error"               # sync, then search the SQLite index
ttydeck search --no-sync "error"     # fast search using the existing index
ttydeck search --files "error"       # compatibility fallback: raw line scan
ttydeck search --json "error"

# ZMODEM upload/download (remote needs lrzsz: rz/sz)
ttydeck zmodem put prod-db ./backup.tar
ttydeck zmodem get prod-db /var/log/syslog

# Automation
ttydeck run prod-db uptime
ttydeck snippet set disk "df -h"
ttydeck snippet run disk prod-db prod-cache
ttydeck snippet list
ttydeck macro list
ttydeck macro run login prod-db
ttydeck audit --limit 50
ttydeck audit --json
ttydeck audit clear
```

### Macros

Macros are portable JSON documents stored in SQLite. They are validated before
they are saved. A macro contains ordered `send`, `wait`, and `sleep` steps:

```json
{
  "version": 1,
  "steps": [
    {"kind": "wait", "pattern": "login: ", "timeout": "10s"},
    {"kind": "send", "send": "deploy", "newline": true},
    {"kind": "wait", "pattern": "Password: ", "timeout": "10s"},
    {"kind": "send", "secret_env": "TTYDECK_DEPLOY_PASSWORD", "newline": true},
    {"kind": "wait", "pattern": "\$ $", "timeout": "10s"},
    {"kind": "send", "send": "uptime", "newline": true},
    {"kind": "sleep", "sleep": "500ms"}
  ]
}
```

```bash
ttydeck macro set login ./login.json
ttydeck macro show login
ttydeck macro run --timeout 30s login prod-db
```

`secret_env` stores only the environment-variable name. The secret value is
resolved immediately before execution and is never written to SQLite, logs, or
command arguments. `newline: true` appends a newline unless the value already
ends with one. Plain `send` steps are for non-secret commands; a macro is not an
encrypted vault.

Use `ttydeck macro run -json login prod-db` for a structured failure diagnostic.
In the TUI, press `m` on a selected host to choose and run a stored macro.
Macro lifecycle, expect matches, timeouts, retries, and failures are persisted in
the local audit log without command text or terminal output.

Host edits show a unified diff before writing. Press `enter` or `y` to apply,
`n`/`esc` to return to the form. The preview carries a content hash so a file
changed by another process is rejected instead of silently applying a stale diff.

### Keys

| Key | Action |
|---|---|
| `enter` | connect |
| `/` | focus filter |
| `j` / `k` | move |
| `g` / `G` | first / last |
| `ctrl+d` / `ctrl+u` | page down / up |
| `r` | reload config |
| `a` | toggle servers / all |
| `f` | toggle favorite |
| `e` | edit host and review the diff |
| `n` | edit notes |
| `m` | run an automation macro |
| `R` | toggle session recording |
| `?` | help |
| `q` | quit |

The initial discovery pass reads `~/.ssh/config`, expands `Include`, and hides
Git/Auth entries (`github.com`, `gitlab.com`, `aur.archlinux.org`, wildcard
blocks, `Match` blocks) from the default server list. Use `-all` to inspect them.

`aur.archlinux.org` is a Git/Auth endpoint, not an interactive shell. Use:

```bash
ttydeck run aur.archlinux.org help
```

For machine-readable connection diagnostics:

```bash
ttydeck diagnose -json aur.archlinux.org
```

This reports the failing stage (`dial`, `handshake`, `auth`, `host_key`,
`pty`, `session`), a category, and a hint without running an interactive shell.

## Status

Early V0.1 development: the SSH-first TUI, safe config editing, metadata,
session recording/replay/search, SQLite FTS5 transcript search, default
plain-text transcript redaction, snippets, expect-style macros, persisted
automation audit logs, safe diff-previewed config edits, ZMODEM transfer, and
release packaging are functional. See [docs/plan.md](docs/plan.md) for
remaining hardening and future connectors.

Recordings are asciicast v2 files plus a redacted `.txt` transcript. The cast is
kept for faithful playback; the transcript is the searchable, redacted form.
The FTS5 index uses the trigram tokenizer, so English and CJK substrings can be
searched. Indexed transcript IDs are scoped to their recording root, preventing
same-named files in different directories from overwriting each other.
Password authentication input is not recorded, but avoid typing secrets into an
interactive shell when recording is enabled.

## License

MIT. See [LICENSE](LICENSE).

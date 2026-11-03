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
```

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
| `?` | help |
| `q` | quit |

The initial discovery pass reads `~/.ssh/config`, expands `Include`, and hides
Git/Auth entries (`github.com`, `gitlab.com`, wildcard blocks, `Match` blocks)
from the default server list. Use `-all` to inspect them.

## Status

Bootstrapping. No usable release yet.

## License

TBD.

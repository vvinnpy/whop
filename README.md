# whop

**A fast, local-first terminal connection deck.**

`whop` is a cross-platform TUI for managing terminal connections, sessions, and automation.
It is designed to be a single pure-Go binary: no CGO, no external `ssh`/`telnet`/`picocom`/`asciinema`
runtime dependencies.

> 完整中文使用手册（含真实 TUI 截图、ProxyJump 原理、录制/宏/vault/ZMODEM 等高级功能）：[docs/usage.md](docs/usage.md)

## Why whop

Existing connection managers are good at one or two things, but rarely all of them:

- fast fuzzy launcher
- safe `~/.ssh/config` editing
- SSH, serial, and telnet in one place
- session recording and searchable transcripts
- macros and expect-style automation
- local-first storage, headless CLI, and machine-readable output

`whop` is the attempt to combine those strengths without becoming an operations platform.

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

```bash
make build
./bin/whop version   # exact tag (v0.2.1) or a Go pseudo-version after it
```

## Releasing

Releases are tag-driven and gated by CI. A release tag must be valid SemVer
(for example `v0.1.0`) and must point to a commit on `main`.

1. Wait for CI on `main` to pass.
2. Create and push the release tag:

   ```bash
   git tag -a v0.1.0 -m "whop v0.1.0"
   git push origin v0.1.0
   ```

The `release` workflow then runs the full CI suite, verifies that the tag is on
`main`, and only after those checks pass runs GoReleaser. GoReleaser publishes
Linux, macOS, and Windows archives for `amd64` and `arm64`, plus
`checksums.txt`. When the `AUR_SSH_PRIVATE_KEY` repository secret is configured,
it also updates the `whop-bin` AUR package.

The initial AUR package must be submitted manually; see
[`packaging/aur/README.md`](packaging/aur/README.md) for the setup steps.

To validate release packaging locally without publishing:

```bash
goreleaser check
goreleaser release --snapshot --clean
```

## Arch Linux (AUR)

`whop-bin` is available from the AUR:

```bash
yay -S whop-bin
```

## Usage

```bash
# Discover commands and command-specific flags
whop help
whop help connect

# Interactive host list
whop

# ssh-agent is optional and disabled by default
whop -agent
WHOP_SSH_AGENT=1 whop
whop -no-agent

# Start with an initial filter
whop prod

# Machine-readable host list
whop list --json

# Connect from the CLI
whop connect prod-db

# Local metadata
whop favorite prod-db on
whop tag prod-db prod db
whop note prod-db "primary database"
whop access github.com service
whop access prod-db auto
whop record prod-db on

# Port forwarding: equivalent to ssh -N -L/-R/-D
whop forward -L 127.0.0.1:8080:db.internal:5432 prod-db
whop forward -R 9000:localhost:3000 prod-db
whop forward -D 127.0.0.1:1080 prod-db
# Full OpenSSH comparison: docs/forwarding.md

# Dual-pane SFTP browser
# tab: switch pane | /: search | ctrl+d/u: page | enter: open | backspace/h: parent | u/d: transfer | ?: help
whop sftp prod-db

# Record, replay, and search sessions
whop -record
whop connect -record prod-db
whop replay ~/.config/whop/recordings/prod-db-*.cast
whop index                        # rebuild and prune the FTS5 index
whop index --no-prune             # rebuild without removing stale entries
whop search "error"               # sync, then search the SQLite index
whop search --no-sync "error"     # fast search using the existing index
whop search --files "error"       # compatibility fallback: raw line scan
whop search --json "error"

# ZMODEM upload/download (remote needs lrzsz: rz/sz)
whop zmodem put prod-db ./backup.tar
whop zmodem get prod-db /var/log/syslog

# In an interactive session:
#   remote `rz` -> whop prompts for local files
#   remote `sz FILE` -> whop downloads to ~/Downloads/whop/<alias>/

# Encrypted host-password vault
whop vault init                 # master password mode
whop vault init --auto-unlock   # optional zero-prompt mode, 0600 key file
whop vault set 232
whop vault list
whop vault status

# Migrate an existing nccm YAML configuration
whop import nccm --json              # dry-run: preview additions and warnings
whop import nccm --only 10.215.211.232 --apply
whop import nccm --apply             # import hosts and available passwords

# Automation
whop run prod-db uptime
whop snippet set disk "df -h"
whop snippet run disk prod-db prod-cache
whop snippet list
whop macro list
whop macro run login prod-db
whop audit --limit 50
whop audit --json
whop audit clear
```

### Port forwarding

`whop forward` mirrors OpenSSH's common forwarding modes without opening a remote shell:

| whop | OpenSSH equivalent | Listener | Fixed destination |
|---|---|---|---|
| `whop forward -L spec alias` | `ssh -N -L spec host` | local | remote side |
| `whop forward -R spec alias` | `ssh -N -R spec host` | remote | local side |
| `whop forward -D spec alias` | `ssh -N -D spec host` | local SOCKS5 | no, selected per SOCKS5 request |

See [docs/forwarding.md](docs/forwarding.md) for side-by-side syntax, data paths, TUI fields, sshd requirements, limitations, and troubleshooting.

### Macros

Macros are portable JSON documents stored in SQLite. They are validated before
they are saved. A macro is one JSON object (not JSONL); its `steps` array contains
ordered `send`, `wait`, and `sleep` operations:

```json
{
  "version": 1,
  "steps": [
    {"kind": "wait", "pattern": "login: ", "timeout": "10s"},
    {"kind": "send", "send": "deploy", "newline": true},
    {"kind": "wait", "pattern": "Password: ", "timeout": "10s"},
    {"kind": "send", "secret_env": "WHOP_DEPLOY_PASSWORD", "newline": true},
    {"kind": "wait", "pattern": "\$ $", "timeout": "10s"},
    {"kind": "send", "send": "uptime", "newline": true},
    {"kind": "sleep", "sleep": "500ms"}
  ]
}
```

```bash
whop macro set login ./login.json
whop macro show login
whop macro run --timeout 30s login prod-db
```

`secret_env` stores only the environment-variable name. The secret value is
resolved immediately before execution and is never written to SQLite, logs, or
command arguments. `newline: true` appends a newline unless the value already
ends with one. Plain `send` steps are for non-secret commands; a macro is not an
encrypted vault.

Use `whop macro run -json login prod-db` for a structured failure diagnostic.
In the TUI, press `m` on a selected host to choose and run a stored macro. After
the macro finishes, its output remains on screen; press Enter to return to the
host list.
Macro lifecycle, expect matches, timeouts, retries, and failures are persisted in
the local audit log without command text or terminal output.

See [docs/macros.md](docs/macros.md) for the full field reference, execution
semantics, JSON escaping rules, and annotated examples.

### nccm migration

`whop import nccm` auto-detects the normal nccm config locations and is
dry-run by default. It imports connection fields, keepalive, identity files,
comments, `-z ssh-rsa` compatibility, and safe custom arguments. Existing aliases
and identical endpoints are skipped. Use `--only name1,name2` to migrate selected
connections and `--apply` to write.

When applying, nccm `-P` password values are stored in the encrypted vault, not
in `~/.ssh/config` or SQLite. The vault master password is requested once per
process, after which whop can authenticate to all stored hosts without asking
for their individual passwords.

```bash
WHOP_VAULT_PASSWORD='...' whop connect 232
WHOP_VAULT_PASSWORD='...' WHOP_HOST_PASSWORD='...' whop vault set 232
```

Environment variables are intended for automation. Interactive use prompts with
hidden input. The master password is never persisted; losing it makes the vault
unrecoverable.

`vault init --auto-unlock` is an explicit convenience mode for users who do not
want to unlock the vault on every CLI process. It generates a random 0600 local
key file next to the vault and uses it automatically. The vault ciphertext is
still encrypted, but the key file lowers protection against malware running as
the same logged-in user. The stronger master-password mode remains the default.

Press `N` to create a host. The form covers the `Host` name/alias, `HostName`,
`User`, `Port`, `ProxyJump`, `IdentityFile`, tags, and an optional vault password.
Creating a host also shows a unified diff before writing. Press `enter` or `y` to
apply, `n`/`esc` to return to the form. The preview carries a content hash so a
file changed by another process is rejected instead of silently applying a stale
diff.

Select a host and press `c` to duplicate it. The create form is prefilled with
its connection fields (including the resolved `IdentityFile` when there is
exactly one) and tags, while `Name` gets a unique suggestion such as
`prod-db-copy`. Prefilled fields are selected on focus, so typing replaces the
old value; change `Name` and `HostName`, then save and review the same diff.
Passwords, favorites, notes, and connection history are intentionally not copied.

The same form edits existing hosts when you press `e`. Renaming `Name` changes
the `Host` alias and migrates favorites, notes, tags, and any stored vault password
to the new alias.

`IdentityFile` may be left blank. For a new host this omits the directive, so
OpenSSH inheritance applies. To use one default key for all new hosts, add this
to `~/.ssh/config`:

```sshconfig
Host *
  IdentityFile ~/.ssh/id_ed25519
```

The same form has a masked `Password` field. Leaving it blank keeps the existing
stored password. A non-empty value is written only to the encrypted local vault;
it is never added to `~/.ssh/config`, SQLite, or logs. The vault must already be
initialized (`whop vault init`), and it is opened with `WHOP_VAULT_PASSWORD`, the
auto-unlock key file, or a hidden master-password prompt.

### Filter mode

Whitespace-separated terms are ANDed, matching is case-insensitive substring
based. Three prefixes exist:

- `tag:prod` — host tags
- `user:deploy` — login user
- `host:db` — alias or hostname

A plain term (no prefix) matches alias, hostname, user, port, kind, tags, and
notes. Examples: `tag:prod db`, `user:admin 10.1.`.

While the search input is focused, printable characters are always search text.
Press `tab` or `esc` to apply the search and return to list action mode. The
query and filtered results remain active, so `e`, `n`, `m`, `f`, and `N` work on
the selected host. In list action mode, `esc` clears the active filter; use `q`
to quit.

### Keys

| Key | Action |
|---|---|
| `enter` | connect |
| `/` | focus search |
| `j` / `k` | move |
| `ctrl+n` / `ctrl+p` | move down / up |
| `g` / `G` | first / last |
| `ctrl+d` / `ctrl+u` | page down / up |
| `r` | reload config |
| `a` | toggle login hosts / all entries |
| `L` | set login access |
| `f` | toggle favorite |
| `N` | create a host and review the diff |
| `c` | copy the selected host and review the diff |
| `e` | edit host name, identity, tags, password, and review the diff |
| `n` | edit notes |
| `p` | configure local/remote/dynamic port forwarding |
| `s` | open the dual-pane SFTP browser |
| `m` | run an automation macro |
| `R` | toggle per-host recording policy |
| `?` | help |
| `tab` / `esc` | apply search and return to list actions |
| `q` | quit |

The list `FLAGS` column keeps row-level status together: `*` marks favorites
and `REC` marks hosts whose recording policy is on for the next session (not an
active recording). Narrow terminals use the compact `FLG` header and `R`
recording marker. The bottom line is only for events: results expire after a
few seconds, progress stays until completion, and red `! ...` errors remain
until the next action or `esc`. See [the status-line guide](docs/usage.md#状态栏与主机标记)
for the full meaning and lifecycle.

The initial discovery pass reads `~/.ssh/config`, expands `Include`, and hides
service endpoints inferred from known Git/Auth hosts, wildcard blocks, and
`Match` blocks from the default login list. Use `-all` to inspect them. Press
`L`, or run `whop access <alias> auto|login|service`, to correct the inference.

`aur.archlinux.org` is a Git/Auth endpoint, not an interactive shell. Use:

```bash
whop run aur.archlinux.org help
whop access aur.archlinux.org service
```

For machine-readable connection diagnostics:

```bash
whop diagnose -json aur.archlinux.org
```

This reports the failing stage (`dial`, `handshake`, `auth`, `host_key`,
`pty`, `session`), a category, and a hint without running an interactive shell.

## Status

Early V0.1 development: the SSH-first TUI, safe config editing, metadata,
session recording/replay/search, per-host recording policy, SQLite FTS5
transcript search, default plain-text transcript redaction, local/remote/dynamic
port forwarding, dual-pane SFTP transfer, snippets, expect-style macros,
persisted automation audit logs, safe diff-previewed config edits, nccm
migration, ZMODEM transfer, and release packaging are functional. See
[docs/plan.md](docs/plan.md) for remaining hardening and future connectors.

Host passwords are stored only as authenticated ciphertext in the local vault,
using Argon2id and AES-256-GCM. They are never written to SSH config, SQLite,
audit events, JSON output, or command-line arguments. The vault protects data at
rest; it cannot protect an unlocked process from malware running as the same
user.

Recordings are asciicast v2 files plus a redacted `.txt` transcript. The cast is
kept for faithful playback; the transcript is the searchable, redacted form.
The transcript applies backspace/delete line editing so deleted input is not
concatenated, while the cast retains the original terminal stream.
The FTS5 index uses the trigram tokenizer, so English and CJK substrings can be
searched. Indexed transcript IDs are scoped to their recording root, preventing
same-named files in different directories from overwriting each other.
Password authentication input is not recorded, but avoid typing secrets into an
interactive shell when recording is enabled.

## License

MIT. See [LICENSE](LICENSE).

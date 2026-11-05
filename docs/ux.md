# whop UX specification

## Principles

1. **Search and go.** Press `/`, type to filter, and `enter` connects.
2. **One screen.** The common path — find host, inspect, connect — fits on one screen.
3. **Keyboard first.** Every action is reachable without a mouse.
4. **Quiet chrome.** No heavy borders, no decorative panels, no wasted vertical space.
5. **Progressive detail.** A compact right-side detail panel on wide terminals; a single
   summary line on narrow terminals.
6. **Predictable keys.** Vim-style navigation plus nccm-compatible filter behavior.
7. **Choices over typing.** Fields with a finite set of values (forward type, access
   mode, and similar enums) must use a selector, never require users to type the
   enum value.

## Main screen

```text
 whop  12 hosts  ~/.ssh/config                              login hosts

 / search: prod█

 NAME             HOST                 USER      PORT   TAGS
 > prod-db        10.10.0.11           deploy    22     prod db
   prod-cache     10.10.0.12           deploy    22     prod
   staging-db     10.20.0.11           deploy    22     staging

 ┌─ prod-db ───────────────────────────────────────────────┐
 │ host      10.10.0.11:22                                 │
 │ user      deploy                                        │
 │ identity  ~/.ssh/id_ed25519                             │
 │ proxy     none                                          │
 └─────────────────────────────────────────────────────────┘

 enter: connect   /: search   j/k: move   ?: help   q: quit
```

The detail panel is hidden below 96 columns. The list remains the primary surface.

## Keys

| Key | Action |
|---|---|
| `enter` | connect to selected host |
| `/` | focus search |
| any printable character | search immediately (when search is focused) |
| `tab` / `esc` | apply search and return to list action mode |
| `esc` in list mode | clear active search |
| `backspace` | delete last search character |
| `ctrl+u` in search mode | clear search and stay in search mode |
| `j` / `down` / `ctrl+n` | next host |
| `k` / `up` / `ctrl+p` | previous host |
| `g` / `G` | first / last host |
| `ctrl+d` / `ctrl+u` | page down / page up |
| `r` | reload config |
| `a` | toggle login hosts/all entries |
| `L` | set login access |
| `N` | create a host |
| `c` | copy selected host into the create form |
| `e` | edit selected host |
| `p` | configure local/remote/dynamic forwarding |
| `s` | open the dual-pane SFTP browser |
| `R` | toggle per-host recording policy |
| `?` | help overlay |
| `q` / `ctrl+c` | quit |

## nccm compatibility

- Press `/` to focus search; typing then filters without opening a dialog.
- `enter` connects immediately.
- `tab` or `esc` applies the filter and returns to list action mode, so `e`,
  `n`, `m`, `f`, and `N` can operate on the filtered result without typing into
  the filter.
- In list action mode, `esc` clears the active filter. Use `q` to quit.
- Initial CLI arguments act as an initial filter:
  `whop prod` opens with `prod` already applied.
- If the initial filter matches exactly one host, the user can press `enter`
  immediately; auto-connect is a later opt-in flag.

## SFTP browser

- The left pane is local and the right pane is remote. `tab` switches the active pane.
- Movement, search, and paging apply only to the active pane. Each pane keeps its own
  query and cursor when the user switches sides.
- `/` filters the active pane by filename; `ctrl+u` clears the query while typing,
  and `tab` / `esc` applies it. Directory navigation clears only the active query.
- `ctrl+d` / `ctrl+u` page the active pane while not searching. `g` / `G` jump to
  the first or last visible entry.
- `backspace` / `left` / `h` returns to the parent directory. `enter` / `right`
  enters directories. `u` uploads the selected local file to the
  remote current directory; `d` downloads the selected remote file to the local
  current directory.
- The footer follows the current mode, and `?` opens the full shortcut reference.

## Visual rules

- One accent color; selection uses accent background.
- Dim secondary metadata; never rely on color alone.
- ASCII fallback for icons and separators.
- No mouse dependency.
- Help is one keystroke away and never blocks the list permanently.
- Errors appear in a single status line, not a modal unless destructive.

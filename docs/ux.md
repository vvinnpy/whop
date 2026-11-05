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
 whop / hosts                    ~/.ssh/config                 login hosts
 / search: prod_                                                   1/4 hosts
------------------------------------------------------------------------------
 FLAGS  NAME             HOST                 TAGS        | PROD-DB
 > *    prod-db          10.10.0.11           prod,db     | host      10.10.0.11:22
   REC  prod-cache       10.10.0.12           prod        | user      deploy
     -  staging-db       10.20.0.11           staging     | identity  ~/.ssh/id_ed25519
                                                           | tags      prod, db
                                                           | favorite  yes
------------------------------------------------------------------------------
  REC on - prod-db (next session)
  enter connect   / search   j/k move   a all entries   ? help   q quit
```

The detail panel is hidden below 100 columns. The list remains the primary surface.
At narrower widths the selected host is summarized in one reserved row between
the list and the bottom rule.

Row-level status is shown in the `FLAGS` column: `*` means favorite and `REC`
means the host will record the session. Hosts without flags show `-`. Narrow
terminals use the compact `FLG` header and `R` recording marker.

## Layout system

Every persistent screen uses the same vertical grid:

```text
header
[search/filter]
rule
body
rule
status (reserved, one row)
footer (one row, never wraps)
```

- The body is sized from the terminal height after reserving header, rules,
  status, and footer.
- The status row sits immediately above the footer. Dynamic notices never move
  the footer or change the body height.
- A full-width horizontal rule separates chrome from the body and the body from
  the status row.
- Split views use one vertical rule with one column of padding on each side.
- The footer is a single line. When space is constrained, lower-priority hints
  are dropped; `? help` and `q quit` remain visible.
- Forms, diff, help, and SFTP use the same grid, so transitions between screens
  do not change the location of the status or shortcut row.

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

## Status line contract

The status row is for events, not persistent host state.

- Persistent host state stays on the row (`FLAGS`) and in the selected-host detail
  or narrow summary. `*` means favorite; `REC` means recording is enabled for
  the next session. `REC` does not mean a recording is currently active.
- Completed actions are short results such as `REC on · prod-db (next session)`.
  They expire after about three seconds and are cleared immediately by
  navigation, search input, or a mode change.
- Operations in progress use `...`, such as `connecting · prod-db...` or
  `uploading file...`, and remain until a result or error replaces them.
- Errors use a red `! ...` line and remain visible across navigation until
  `esc` dismisses them or a later status-producing action replaces them. A
  scoped error names its target host.
- A host-scoped result is not shown after the selected host changes. The
  relaunched TUI's initial notice is the exception: it remains visible on
  startup because the cursor may start on a different row, and its text names
  the target host.
- The status row is reserved even when empty so notices do not shift the list
  or move the footer. It is rendered directly above the footer.

## Visual rules

- One accent color; selection uses accent background and a row marker.
- Dim secondary metadata; never rely on color alone.
- ASCII fallback for icons and separators. Unicode rules use `─` / `│`;
  fallback is `-` / `|`.
- Tables use two-column gaps and right-align ports and sizes.
- Avoid full panel borders except for destructive confirmation dialogs.
- No mouse dependency.
- Help is one keystroke away and never blocks the list permanently.
- Errors appear in a single status line, not a modal unless destructive.

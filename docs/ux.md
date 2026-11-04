# whop UX specification

## Principles

1. **Type and go.** Like nccm, typing filters immediately and `enter` connects.
2. **One screen.** The common path — find host, inspect, connect — fits on one screen.
3. **Keyboard first.** Every action is reachable without a mouse.
4. **Quiet chrome.** No heavy borders, no decorative panels, no wasted vertical space.
5. **Progressive detail.** A compact right-side detail panel on wide terminals; a single
   summary line on narrow terminals.
6. **Predictable keys.** Vim-style navigation plus nccm-compatible filter behavior.

## Main screen

```text
 whop  12 hosts  ~/.ssh/config                         all | servers

 / filter: prod█

 NAME             HOST                 USER      PORT   TAGS
 > prod-db        10.10.0.11           deploy    22     prod db
   prod-cache     10.10.0.12           deploy    22     prod
   staging-db     10.20.0.11           deploy    22     staging

 ┌─ prod-db ───────────────────────────────────────────────┐
 │ host      10.10.0.11:22                                 │
 │ user      deploy                                        │
 │ identity  ~/.ssh/id_ed25519                             │
 │ proxy     none                                          │
 │ source    ~/.ssh/config                                 │
 └─────────────────────────────────────────────────────────┘

 enter connect   / filter   j/k move   g/G top/bottom   r reload   ? help   q quit
```

The detail panel is hidden below 96 columns. The list remains the primary surface.

## Keys

| Key | Action |
|---|---|
| `enter` | connect to selected host |
| `/` | focus filter |
| any printable character | filter immediately (when filter focused) |
| `esc` | clear filter; if empty, quit |
| `backspace` | delete last filter character |
| `ctrl+u` | clear filter |
| `j` / `down` | next host |
| `k` / `up` | previous host |
| `g` / `G` | first / last host |
| `ctrl+d` / `ctrl+u` | page down / page up |
| `r` | reload config |
| `a` | toggle servers/all hosts |
| `?` | help overlay |
| `q` / `ctrl+c` | quit |

## nccm compatibility

- Typing filters without opening a dialog.
- `enter` connects immediately.
- Initial CLI arguments act as an initial filter:
  `whop prod` opens with `prod` already applied.
- If the initial filter matches exactly one host, the user can press `enter`
  immediately; auto-connect is a later opt-in flag.

## Visual rules

- One accent color; selection uses accent background.
- Dim secondary metadata; never rely on color alone.
- ASCII fallback for icons and separators.
- No mouse dependency.
- Help is one keystroke away and never blocks the list permanently.
- Errors appear in a single status line, not a modal unless destructive.

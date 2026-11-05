# ADR 0005: Keep the TUI visible until an SSH session is ready

- Status: Accepted
- Date: 2026-11-05

## Context

Pressing `enter` used to quit Bubble Tea immediately, restore the terminal, and
then dial SSH. While DNS, TCP, the SSH handshake, host-key verification, or
authentication was in progress the host list was gone, so a slow or unreachable
server looked like a frozen application until the connection timed out.

The terminal cannot be shared between Bubble Tea and an interactive remote shell.
The handover must happen before raw input and remote output are forwarded, but it
should happen only after the connection is known to be usable.

## Decision

Split interactive SSH startup into two phases:

1. `Runner.Prepare` dials, authenticates, opens a session, requests a PTY, starts
   the remote shell, and buffers any output produced before handover.
2. `PreparedSession.Run` restores the local terminal, replays buffered output,
   switches stdin/output to the remote shell, and waits for it to exit.

The TUI runs `Prepare` as a Bubble Tea command. It remains visible and shows
`connecting · <host>...` while this happens. A failure is rendered in the same
TUI and the list remains interactive. Only a successful preparation emits a
message that quits Bubble Tea and hands the terminal to the prepared session.

Authentication that requires terminal input (password, keyboard-interactive/OTP,
or vault unlock) is detected with a promptless provider. `Prepare` returns
`ErrInteractiveAuthRequired`; the TUI then restores the terminal and falls back
to the existing fully interactive connection path, where prompts are safe.

## Consequences

Positive:

- Slow and failed connections no longer hide the host list.
- DNS/connect/handshake/auth failures appear in the normal status line.
- Remote banners and the first prompt are not lost during handover.
- Stored-password and key-based hosts can connect without leaving the TUI until
  the shell is ready.

Negative:

- The SSH runner has an additional prepared-session lifecycle that must be
  closed on every error path.
- Interactive credential prompts still require temporarily leaving the TUI
  because Bubble Tea and terminal password readers cannot own stdin at once.
- Output before handover is buffered in memory; the window is short but a remote
  endpoint that emits unbounded data during preparation can consume memory.

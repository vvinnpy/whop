# ADR 0003: Local encrypted password vault

- Status: Accepted
- Date: 2026-11-04

## Context

nccm configurations may contain SSH passwords in `customargs`. Repeatedly asking
for each host password is unacceptable, but storing plaintext passwords in
`~/.ssh/config`, SQLite, logs, or process arguments violates ttydeck's security
contract. A portable pure-Go design is required for Linux, macOS, and Windows.

## Decision

Store host passwords in a local encrypted vault at the platform user config
directory:

```text
~/.config/ttydeck/secrets.vault
```

The vault uses:

- Argon2id with time=3, memory=64 MiB, threads=2
- AES-256-GCM with a fresh random nonce on every save
- authenticated binary header and JSON payload
- mode `0600`, same-directory temporary file, fsync, atomic replace, and flock
- no plaintext password, master password, or derived key on disk

The master password is requested once per ttydeck process. `TTYDECK_VAULT_PASSWORD`
may provide it for automation. The vault is lazy-opened only when SSH password
authentication is actually attempted.

nccm `-P` values are parsed with shell-aware quoting rules and stored in the vault
only when `import nccm --apply` is requested. Dry-run never creates or modifies
the vault.

## Consequences

Positive:

- Host passwords can be configured once and reused without repeated prompts.
- Passwords do not enter SSH config, SQLite, JSON output, audit logs, or CLI args.
- The implementation is pure Go and cross-platform.

Negative:

- Losing the master password makes the vault unrecoverable.
- Users must unlock the vault once per process unless automation supplies the
  master password through the environment.
- The vault protects data at rest; it does not defend against malware running as
  the same logged-in user while the process is unlocked.

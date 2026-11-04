# ADR 0002: Use a pure-Go YAML library for nccm import

- Status: Accepted
- Date: 2026-11-04

## Context

ttydeck needs to import existing nccm connection definitions so users can migrate
without manually rewriting every Host block. nccm stores its configuration as YAML,
including quoted keys, comments, custom arguments, and nested keyboard settings.
A line-oriented parser would be brittle around quoting and YAML syntax.

## Decision

Use `go.yaml.in/yaml/v3`, a pure-Go YAML parser, only in the optional
`internal/nccm` import path. Import is dry-run by default and writes through the
same locked, backed-up, atomic SSH config append path as other ttydeck edits.

Imported data is limited to safe SSH connection fields. nccm password arguments
are never persisted. Legacy `-z ssh-rsa` and `-z expect-ssh-rsa` modes map only
to explicit SHA-1 RSA compatibility settings; password-mode hosts are imported
without a password and must prompt interactively.

## Consequences

Positive:

- Existing nccm configs can be migrated reliably without shelling out to nccm or yq.
- YAML quoting, Unicode, comments, and nested global settings are handled correctly.
- The dependency remains compatible with `CGO_ENABLED=0` and all target platforms.

Negative:

- The binary gains a small pure-Go YAML dependency.
- Arbitrary nccm custom arguments cannot always be mapped to typed SSH config;
  unsupported flags are reported and omitted rather than guessed.
- Password-based nccm automation cannot be migrated automatically; ttydeck uses
  interactive credential entry or its future vault instead.

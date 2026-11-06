# AUR packaging

The AUR package is `whop-bin`. It installs the Linux `amd64` or `arm64`
binary from the matching GitHub release asset and does not require a Go
toolchain on the target machine.

## Initial AUR submission

The first AUR repository must be created through the AUR web interface. Use
`whop-bin/PKGBUILD` as the initial submission, then publish the generated
`.SRCINFO`:

```bash
cd packaging/aur/whop-bin
makepkg --printsrcinfo > .SRCINFO
```

After the package exists, future stable releases are generated and pushed by
GoReleaser through the `aurs` section in `.goreleaser.yaml`.

## Release automation

Add an SSH private key for the AUR account to the GitHub repository as
`AUR_SSH_PRIVATE_KEY`. The release workflow passes it to GoReleaser as
`AUR_KEY`.

The key must not have a passphrase. GoReleaser only publishes stable releases
when `AUR_KEY` is present; snapshot builds and releases without the secret
generate the PKGBUILD and `.SRCINFO` without pushing.

## Installing

Once the package has been submitted:

```bash
yay -S whop-bin
```

or with any other AUR helper.

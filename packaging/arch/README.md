# Arch Linux packages

Until NepFleet is on the AUR, build and install from here:

```sh
git clone https://github.com/Serraniel/nepfleet && cd nepfleet/packaging/arch/nepfleet-bin
makepkg -si
```

- **`nepfleet-bin`**: the CLI from a GitHub release, for x86_64 and aarch64. The checksums are the release's `SHA256SUMS`, and the binary goes in unchanged (`!strip`), so it stays the one that was signed.
- **`nepfleet-git`**: built from main, for testing before a release. Its version is `edge`: it installs and updates machines (Fleets → Update NepFleet) to the `:edge` image of main, after checking that the edge workflow signed it. It replaces `nepfleet-bin`; install that again to go back to releases.
- **The desktop app** has its own package in [nepfleet-ui](https://github.com/Serraniel/nepfleet-ui/tree/main/packaging/arch).

A new release needs `pkgver` and the checksums updated, then `makepkg --printsrcinfo > .SRCINFO`.

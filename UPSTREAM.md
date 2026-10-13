# Maintaining this fork

This project uses [K4ryuu/CS2-Egg-Go](https://github.com/K4ryuu/CS2-Egg-Go) as its
implementation base. The imported baseline is
`70e8d88bb5838d36d82e14a7f397ee1210a2ebc5` (2026-10-13). The former independent
implementation remains in repository history through
`ed8a8e7098ed985cd142fba57d844269b0e70796`.

Keep the node core, Docker discovery, protocol, shared mounts, installer and
existing node modules aligned with upstream. The Go module path and binary names
remain upstream-compatible to avoid a repository-wide rename on every update.
The GitHub repository remains at the existing S2Script URL; importing upstream
source does not change GitHub's repository-network/fork badge.

## Downstream changes

- S2Script replaces CounterStrikeSharp, ModSharp and Swiftly.
- S2Script startup auto-updates, bundled plugin controls and registry management.
- MultiAddonManager and GameFixes installers; private configuration is retained.
- S2Script panel exports, branding, images, release publishing and self-update URL.
- A small bootstrap downloads/verifies the node binary and runs the upstream Go
  wizard. It is not a separate Python provisioning implementation.

## Updating the baseline

Fetch upstream into a separate branch and inspect its changes against the exact
baseline above. Apply upstream changes with the downstream patch set in view;
do not reintroduce removed framework controls or the upstream self-update URL.
Record the new baseline here and in NOTICE. Retain copyright and licence notices.

```bash
git remote add upstream https://github.com/K4ryuu/CS2-Egg-Go.git
git fetch upstream main
git diff 70e8d88bb5838d36d82e14a7f397ee1210a2ebc5 upstream/main
make test vet egg node
go test -race ./tests/...
```

If the remote already exists, skip `git remote add`. Native CS2 acceptance still
needs a test node: install the node before creating servers, enable VPK sync,
create a server using the fork's new image, and verify automatic discovery,
read-only sharing, S2Script/plugins, both native addons, and clean Stop/Start.

## Legacy migration and publication

The independent `s2egg`/`s2node` implementation has a different protocol/config
format. Stop its services before installing this `cs2node` generation; do not mix
legacy images with the fork daemon. Keep backups of existing server volumes.
For a node without servers, install the fork daemon first and create servers
afterward. No manual server registrations or panel mounts are required.

Plugin mutations stage and validate downloads, exclude the running game, and
roll back ordinary errors. The former independent durable artifact journal is
not imported. An abrupt process or host failure during a multi-file update can
require repair on the next boot; this is not a crash-atomic transaction engine.

The first fork release is `v0.2.0-alpha.1`. Its binary/asset names follow upstream:
`cs2node_linux_amd64` and `checksums.txt`. Existing 0.1.x release assets cannot run
this installer. The release must be published and its workflow pass before using
the release-download command; until then, `make node` provides a source build.

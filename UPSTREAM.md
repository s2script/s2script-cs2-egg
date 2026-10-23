# Maintaining this fork

This project uses [K4ryuu/CS2-Egg-Go](https://github.com/K4ryuu/CS2-Egg-Go) as its
implementation base. The imported baseline is release **v1.1.0**,
`e5815ae266050e57c2d389f595e2da92b4c1a8b6` (2026-10-23). The former independent
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
- The v1.0.4 Steam client copy optimization compares same-size files by SHA-256
  instead of timestamps, so replacements with preserved or older timestamps are
  installed. Unchanged clients are read for comparison but not rewritten.
- Maintenance statistics snapshots clone maps and result slices under the module
  lock so concurrent status requests cannot race with a scheduled live pass.

## Updating the baseline

Fetch upstream into a separate branch and inspect its changes against the exact
baseline above. Apply upstream changes with the downstream patch set in view;
do not reintroduce removed framework controls or the upstream self-update URL.
Record the new baseline here and in NOTICE. Retain copyright and licence notices.

```bash
git remote add upstream https://github.com/K4ryuu/CS2-Egg-Go.git
git fetch upstream main
git diff e5815ae266050e57c2d389f595e2da92b4c1a8b6 upstream/main
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

Release versions are taken from the published GitHub tag, independently of the local-development `VERSION` fallback. Asset names follow upstream: `cs2node_linux_amd64` and `checksums.txt`. The bootstrap resolves stable/beta metadata to one exact tag and requires both assets; old independent-generation assets cannot run this installer. Egg exports follow `:stable`/`:beta`, with optional operator-selected version tags. No per-release source edits are required.

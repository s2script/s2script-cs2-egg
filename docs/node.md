# Set up the Wings node

`cs2node` is K4ryuu's optional host daemon. Install it on each game node that should share CS2 files. The panel can run on another machine. **You can install it with zero servers.**

## 1. Run the installer

After a release workflow has successfully published the node assets and image:

```bash
curl -fsSL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/scripts/install-node.sh | bash
```

Use an interactive SSH terminal on Debian 11+ or Ubuntu 22.04+. The bootstrap downloads the matching release binary/checksum, verifies it, then starts the Go wizard with sudo. No Go or Python is required on the node. It installs `jq` if needed to resolve the release, and missing `lib32gcc-s1` and `lib32stdc++6` through apt: VPK sync runs SteamCMD on the host, so the container runtime alone is insufficient. See [Debian's 32-bit GCC runtime package](https://packages.debian.org/bookworm/lib32gcc-s1). The default installer selects the latest stable release and its `stable` update channel. For beta, pipe the script into `S2NODE_CHANNEL=beta bash`; beta includes the newest stable releases too. For an exact initial release, use `S2NODE_RELEASE=v1.2.3 bash`. Both downloads are pinned to the same resolved tag before checksum verification.

Other distributions are outside the bootstrap's support. Install their equivalent 32-bit runtime, download and verify the matching node release assets, then run `sudo ./cs2node_linux_amd64 install` directly.

## 2. Answer the wizard

Enable `vpksync` for one shared CS2 install. It downloads CS2 to `/srv/cs2-shared/`. Choose the Valve-update restart policy appropriate for your players. Other upstream modules are optional:

| Module | Purpose |
|---|---|
| `addoncache` | Cache Metamod, S2Script, MultiAddonManager and GameFixes; hold fleet versions |
| `workshop` | Share workshop content |
| `guard` | Host packet filtering; requires a supported kernel |
| `crashes`, `metrics` | Crash evidence and node/server status |
| `backup`, `cleanup`, `consolelog` | Backups, file cleanup and persistent console history |
| `maintenance` | Host image/build-cache pruning, journal retention and disk/reboot alerts; starts as a dry run |
| `alerts` | Discord notifications; requires a webhook |

Keep the S2Script image-discovery prefix. The daemon watches Docker for matching images, including servers created later. You do not register UUIDs, configure Pelican mounts, or rerun the wizard for each server.

The installer writes `/etc/cs2node/config.json`, installs `/usr/local/bin/cs2node`, and starts the `cs2node` systemd service. To change your choices later, run `sudo cs2node install`.

## 3. Wait for CS2, then create a server

```bash
journalctl -u cs2node -f
sudo cs2node status
sudo cs2node doctor
```

The first shared download takes a while. With no servers yet, an empty discovery list is expected. Once ready, [import the egg and create a server](install.md). Start it with the new fork image. The console should report `Daemon-managed game files, SteamCMD skipped`. A server already running when the daemon was installed needs a stop/start to pick up the mount.

If VPK sync is off or the daemon is unavailable, the egg uses a standalone download in the server volume. For a partly downloaded standalone server, stop it, enable node VPK sync and let the central download finish, then start it again. Keep private configs/plugins; do not copy or link an incomplete Steam install into the shared directory.

## Network setup

A dev node can share its public host IP using Docker bridge networking and different allocated ports. Keep the complete `docker.network` mapping correctly indented in `/etc/pelican/config.yml`; verify Wings starts after editing. Never give a macvlan container the host's own IP: that address conflict can disconnect SSH and Wings. The daemon does not require changing a working Wings network.

## Updates and removal

```bash
sudo cs2node update
sudo cs2node uninstall
```

The node follows its own `stable`/`beta` channel, independently of the S2Script framework channel. The old independent `s2node` service/config is incompatible; stop it before installing this generation. See [UPSTREAM.md](../UPSTREAM.md).

After updating an existing node, run `sudo cs2node install` to opt into new
modules such as [maintenance](modules/maintenance.md). Existing configurations
keep their enabled modules. Review maintenance dry-run reports before changing
`dry_run` to `false`; unattended security upgrades use the separate
`sudo cs2node maintenance setup` command.

Uninstall keeps config and shared files. Upstream's `uninstall --purge` also removes config. [Module index](modules/README.md) · [Node config](reference/node-config.md) · [Upgrading](upgrading.md)

# S2Script CS2 Egg

A downstream fork of [K4ryuu/CS2-Egg-Go](https://github.com/K4ryuu/CS2-Egg-Go) for Pelican and Pterodactyl. It keeps upstream startup, node discovery, shared CS2 files and node modules, with **S2Script as the only scripting framework**.

Included: Metamod, the S2Script auto-updater and plugin manager, [MultiAddonManager](https://github.com/Source2ZE/MultiAddonManager), and [GameFixes](https://github.com/SlynxCZ/GameFixes_mm). CounterStrikeSharp, ModSharp and Swiftly installers are removed.

Release versions come from the GitHub release tag. The installer selects the latest stable release and the egg defaults to `:stable`; no per-release edits to version files, installer defaults or egg exports are needed. Wait for the release workflow to succeed before installing. The legacy `:prerelease` image and independent node implementation are a different generation; see [migration notes](UPSTREAM.md).

## 1. Set up the node (optional)

For shared CS2 files, do this **before creating any servers**. Run on the Wings node, not the web-panel machine:

```bash
curl -fsSL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/scripts/install-node.sh | bash
```

The bootstrap verifies the release checksum, installs missing host SteamCMD runtime libraries on Debian/Ubuntu, and opens K4ryuu's Go installer wizard. Enable **VPK sync** for a shared install; the other modules are optional. You need Debian 11+ or Ubuntu 22.04+ on x86-64, Docker/Wings, sudo access, and enough disk space for CS2. The default node-update channel is stable. Use `curl -fsSL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/scripts/install-node.sh | S2NODE_CHANNEL=beta bash` to install and follow beta releases.

No servers need to exist yet. The daemon discovers matching containers automatically and supplies its socket and shared mounts. There is no server-registration command or manual panel mount.

```bash
sudo cs2node status
sudo cs2node doctor
journalctl -u cs2node -f
```

Wait for the first CS2 download to finish before judging the shared-install checks. [Node guide](docs/node.md).

## 2. Import the egg in the panel

In Pelican's admin egg importer, choose **Import from URL** and paste:

```text
https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/pterodactyl/s2script-cs2-egg.json
```

For Pterodactyl, download that JSON and import the file if your panel has no URL option. Create a server with this egg, allocate the game port, and set `STEAM_ACC` to your GSLT. Use `ghcr.io/s2script/s2script-cs2-egg:stable` for stable releases, or `:beta` to follow the newest published release including prereleases.

Start it. The first start installs CS2, Metamod, S2Script and the selected addons. A short panel installation script is intentional: installation runs in the container entrypoint, as in upstream. With a ready node shared install, the server skips its own SteamCMD download.

Prefer a standalone server? Skip step 1; everything installs into its own volume. There is also a [standalone egg](pterodactyl/s2script-cs2-egg-standalone.json).

## 3. Choose addons and plugins

| Panel variable | Default | Purpose |
|---|---|---|
| `INSTALL_S2SCRIPT` | `1` | Install/check S2Script on startup |
| `S2SCRIPT_CHANNEL` | `stable` | Framework channel, separate from the image channel |
| `S2SCRIPT_VERSION` | empty | Optional exact framework release tag |
| `INSTALL_MULTIADDONMANAGER` | `1` | Install/update MultiAddonManager |
| `INSTALL_GAMEFIXES` | `1` | Install/update GameFixes |
| `S2SCRIPT_PLUGIN_*` controls | `default` | Preserve policy/manual choices, or explicitly enable/disable bundled plugins |

Metamod is automatically required by these addons. Plugin downloads, checks, exact pins, dependencies and policy are documented in the [addon and plugin guide](docs/addons.md). Other upstream node modules remain available: [VPK sync](docs/modules/vpk-sync.md), [workshop cache](docs/modules/workshop.md), [guard](docs/modules/guard.md), [backups](docs/modules/backups.md), [metrics](docs/modules/metrics.md), [maintenance](docs/modules/maintenance.md), and [module index](docs/modules/README.md).

For a dev node sharing one public address, use Docker bridge networking with separate allocated ports. Do not assign the host's own address to a macvlan container. Prod nodes with separate container addresses can retain their correctly provisioned network design.

[All documentation](docs/README.md) · [Upstream baseline and migration](UPSTREAM.md) · [Changelog](CHANGELOG.md)

GPL-3.0-or-later. Original code copyright Kőrösfalvi “K4ryuu” Martin; downstream changes by S2Script contributors. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

## Publishing a release

In GitHub, create a release from the desired commit with a tag such as `v1.2.3`. Leave **Set as a pre-release** unchecked for stable; check it for testing releases such as `v1.3.0-rc.1`. Publish it and wait for Actions to finish. Actions builds both programs with that tag's version, attaches the node binary/checksum, and publishes the versioned image and eligible channel aliases.

`VERSION` is only a local-development fallback. Neither it nor `CHANGELOG.md` must be edited to publish. The optional CLI equivalent is `./scripts/release.sh v1.2.3`.

# S2Script CS2 Egg

CS2 server tooling for Linux amd64, Pterodactyl and Pelican, built around **Metamod + S2Script**.

[![License: GPL-3.0-or-later](https://img.shields.io/badge/license-GPL--3.0--or--later-blue?style=flat-square)](LICENSE)
[![Platform: Linux amd64](https://img.shields.io/badge/platform-Linux%20amd64-5865F2?style=flat-square)](docs/install.md)
[![Panels: Pterodactyl + Pelican](https://img.shields.io/badge/panels-Pterodactyl%20%2B%20Pelican-2496ED?style=flat-square)](docs/install.md#pterodactyl-or-pelican)
[![Framework: S2Script](https://img.shields.io/badge/framework-S2Script-2EA043?style=flat-square)](docs/frameworks.md)

**[Install a server](docs/install.md)** · **[Set up shared files on a node](docs/node-setup.md)** · **[Releases](https://github.com/s2script/s2script-cs2-egg/releases)** · **[Documentation](#documentation)**

This is an initial alpha implementation. Automated fixtures and CI image checks
cover the runtime and optional node services. Live CS2, panel, HUD and firewall
acceptance remains unverified; see the [acceptance record](docs/acceptance.md).
Panel installation uses the egg URL below and published GHCR images. The image
channel becomes usable after a release's **public-verification** job succeeds.
[Source builds](docs/install.md#build-from-source)
remain available for development.

## Features

- **One framework stack.** Installs CS2 through SteamCMD, prepares compatible
  Metamod/S2Script versions, supervises the game, and handles graceful shutdown.
- **Installed-plugin updates.** Checks the installed inventory at startup,
  including plugins in `disabled/`. Supports exact pins, installing disabled,
  and all 18 bundled-plugin toggles while inheriting S2Script's defaults.
- **Optional sharing and coordinated updates.** Shares immutable game/VPK
  generations and verified workshop content, caches framework/plugin downloads,
  and schedules updates around player counts or maintenance windows.
- **Discord webhooks.** Reports readiness, shutdown, crashes and selected
  operational outcomes. A documented private API supports a later companion bot.
- **Host operations.** Optional bounded logs, crash reports, consistent backups,
  stopped-server restores, console guard, narrowly scoped firewall enforcement,
  and verified node updates with rollback.

## Get started

Requires a Linux x86-64 Docker host, persistent server storage, a reachable game
allocation and a Steam game server login token (GSLT). The
[installation guide](docs/install.md) covers panel import, permissions, disk
planning, network ports and first-start verification.

1. In the panel's administration area, open **Eggs → Import → From URL**
   (or **Nests → Import Egg**, depending on the panel) and paste:

```text
https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/pterodactyl/s2script-cs2-egg.json
```

2. Create a server using **S2Script CS2 (standalone)**. Select
   **S2Script prerelease** (`ghcr.io/s2script/s2script-cs2-egg:prerelease`) during
   the alpha period. The stable choice needs a published stable release.
3. Assign the game allocation, supply the GSLT privately as `STEAM_ACC`, and
   start the server. First start downloads CS2, Metamod and S2Script.

No local image build or Go installation is needed for the published-image path.
If your panel only accepts file uploads, download the JSON from the same link
and upload it. See [installation](docs/install.md#pterodactyl-or-pelican) for
version-pinned imports and image availability checks.

Leave bundled plugin controls at `default` to inherit S2Script's enablement.
Set a control to `0` or `1` to disable or enable it. Use the
[plugin guide](docs/plugins.md) for inventory, pins and starting plugins disabled.

The `S2EGG_READY` marker and `server.up` event follow a successful local game
query. Verify native plugins separately with `meta list` and actual gameplay.

## Optional node service

The egg works standalone. Add **s2node** when several servers need
shared storage, coordinated updates or host operations. Each module is opt-in;
host credentials remain outside game containers. Communication uses private
Unix sockets and explicit peer identities.

On an **Ubuntu/Debian amd64 Wings node** with `curl`, Python 3.10+ and healthy
Wings/Docker, stop your test server and run:

```bash
curl -fsSL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/2b4505e3acda45c2391ca2f81c4ead36b959479f/scripts/setup-node.py | sudo python3 -
```

The wizard discovers servers, asks for allocations/release, installs s2node and
host SteamCMD, prepares permissions/configuration, and starts an optional
background CS2 build. It shows a summary before applying changes and preserves
existing settings with private backups. You finish by attaching the **read-only
mount** in the panel, then start one server after the build succeeds.

Follow the **[short node setup guide](docs/node-setup.md)** for prompts, the panel
mount values and checks. Shared files are opt-in and local to each node; symlink
mode shares VPKs while keeping configs/plugins private. A partial standalone
download can switch without finishing first. For custom layouts, use the
[manual setup](docs/node-setup-manual.md) and [node reference](docs/node.md).

## Documentation

| Need | Guide |
| --- | --- |
| Install, panel import, debug image | [Installation](docs/install.md) |
| Install the node and switch to shared CS2 files | [Node setup walkthrough](docs/node-setup.md) |
| Settings and precedence | [Configuration](docs/configuration.md) |
| Plugin inventory, pins and disabled state | [Plugins](docs/plugins.md) · [18 default controls](docs/bundled-plugins.md) |
| Metamod, S2Script and optional HUD | [Frameworks](docs/frameworks.md) |
| Process lifecycle and readiness | [Runtime](docs/runtime.md) |
| Shared game/VPK generations | [Shared files](docs/shared-files.md) |
| Scheduled CS2 updates and recovery | [Updates](docs/updates.md) |
| Cache and workshop content | [Workshop](docs/workshop.md) |
| Notifications and future bot API | [Webhooks](docs/webhooks.md) · [Integrations](docs/integrations.md) |
| Logs, backups and cleanup | [Logging](docs/logging.md) · [Backups](docs/backups.md) · [Cleanup](docs/cleanup.md) |
| Optional guard and node updates | [Guard](docs/guard.md) · [Project updates](docs/project-updates.md) |
| Move, diagnose or remove an installation | [Migration](docs/migration.md) · [Troubleshooting](docs/troubleshooting.md) · [Uninstall](docs/uninstall.md) |
| Release assets and evidence | [Releases](docs/releases.md) · [Acceptance](docs/acceptance.md) · [Feature matrix](docs/parity.md) |

## Development

The production Go module uses the standard library. Build and verify locally:

```sh
make test vet build egg-check release-check
go test -race ./...
python3 -m venv /tmp/s2script-schema
/tmp/s2script-schema/bin/pip install -r tests/schema-requirements.txt
make schema-check SCHEMA_PYTHON=/tmp/s2script-schema/bin/python
```

Set `GO=/absolute/path/to/go` when needed. Release checks use temporary Git
fixtures and do not publish anything. See [testing](docs/testing.md),
[contribution guidance](.github/CONTRIBUTING.md) and
[security reporting](.github/SECURITY.md).

## License

Project code is **GPL-3.0-or-later**. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
Downloaded game/framework artifacts and the Go runtime retain their own terms.
Release binaries and images identify their corresponding source revision.

## Credits

[K4ryuu's CS2-Egg-Go](https://github.com/K4ryuu/CS2-Egg-Go) supplied the operational
reference and GPL licensing basis. Its Docker rootfs approach and console
evidence are credited in [NOTICE](NOTICE).
[S2Script](https://github.com/s2script/s2script) supplies the scripting framework
and bundled-plugin defaults; [Metamod:Source](https://www.sourcemm.net/) is its
native dependency. Valve provides CS2 and SteamCMD.

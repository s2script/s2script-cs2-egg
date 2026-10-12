# S2Script CS2 Egg

CS2 server tooling for Linux amd64, Pterodactyl and Pelican, built around **Metamod + S2Script**.

[![License: GPL-3.0-or-later](https://img.shields.io/badge/license-GPL--3.0--or--later-blue?style=flat-square)](LICENSE)
[![Platform: Linux amd64](https://img.shields.io/badge/platform-Linux%20amd64-5865F2?style=flat-square)](docs/install.md)
[![Panels: Pterodactyl + Pelican](https://img.shields.io/badge/panels-Pterodactyl%20%2B%20Pelican-2496ED?style=flat-square)](docs/install.md#pterodactyl-or-pelican)
[![Framework: S2Script](https://img.shields.io/badge/framework-S2Script-2EA043?style=flat-square)](docs/frameworks.md)

**[Install](docs/install.md)** · **[Releases](https://github.com/s2script/s2script-cs2-egg/releases)** · **[Configuration](docs/configuration.md)** · **[Documentation](#documentation)**

This is an initial alpha implementation. Automated fixtures and CI image checks
cover the runtime and optional node services. Live CS2, panel, HUD and firewall
acceptance remains unverified; see the [acceptance record](docs/acceptance.md).
Use the source-build instructions until a release's public-verification job
confirms its downloadable assets and images.

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

Build the runtime image and generate an egg that references it:

```sh
git clone https://github.com/s2script/s2script-cs2-egg.git
cd s2script-cs2-egg
docker build --platform linux/amd64 --target runtime \
  -f docker/Dockerfile -t s2script-cs2-egg:dev .
make egg IMAGE=s2script-cs2-egg:dev
```

`make egg` requires Go **1.27.2** and Make. Import
[pterodactyl/s2script-cs2-egg.json](pterodactyl/s2script-cs2-egg.json) into your
panel and select the image built on the assigned node. Supply the GSLT privately
and start the server. First start downloads CS2 and the selected framework stack.

Leave bundled plugin controls at `default` to inherit S2Script's enablement.
Set a control to `0` or `1` to disable or enable it. Use the
[plugin guide](docs/plugins.md) for inventory, pins and starting plugins disabled.

The `S2EGG_READY` marker and `server.up` event follow a successful local game
query. Verify native plugins separately with `meta list` and actual gameplay.

## Optional node service

The egg works standalone. Add [s2node](docs/node.md) when several servers need
shared storage, coordinated updates or host operations. Each module is opt-in;
host credentials remain outside game containers. Communication uses private
Unix sockets and explicit peer identities.

Start with [shared files](docs/shared-files.md) and [updates](docs/updates.md),
then enable only the modules you need. Node installation stages reviewable
service files; it does not silently enable services or alter your firewall.

## Documentation

| Need | Guide |
| --- | --- |
| Install, panel import, debug image | [Installation](docs/install.md) |
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

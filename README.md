# S2Script CS2 Egg

GPL-3.0-or-later CS2 server tooling for Pterodactyl and Pelican, focused on Metamod and S2Script. The intended repository namespace is `s2script/s2script-cs2-egg`.

**Current milestone:** source-built standalone runtime with SteamCMD bootstrap, supervised readiness/console/status/shutdown, Metamod/S2Script preparation, checks of every installed plugin, bounded Discord lifecycle delivery, and an opt-in debug image target. See [installation and rollback](docs/install.md), [webhook delivery](docs/webhooks.md), [runtime contracts](docs/runtime.md), and [verification/acceptance](docs/testing.md). No public image or downloadable release is published. Local Docker and native CS2/panel acceptance have not been run; CI image-build results must be checked on the actual commit.

## Build and verify

Requires Linux x86-64, Go **1.27.2**, and Make for the convenience targets. This milestone uses only the Go standard library. The pinned official Go archive is `https://go.dev/dl/go1.27.2.linux-amd64.tar.gz`, with SHA-256 `ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5`.

```sh
make test
make vet
make build
make egg-check
```

Set `GO=/absolute/path/to/go` when using a task-local toolchain. Builds use `-trimpath`. Regenerate both artifacts with `make egg`; `IMAGE=your-local-image:tag` selects an image reference. The default `s2script-cs2-egg:dev` is a local development reference, not an existing published image. Build the `runtime` or `debug` target in [docker/Dockerfile](docker/Dockerfile) using the commands in the installation guide.

## Read-only commands

```sh
ALLOW_TOKENLESS=1 ./bin/s2egg config check --root /path/to/server
ALLOW_TOKENLESS=1 ./bin/s2egg config show --root /path/to/server
ALLOW_TOKENLESS=1 ./bin/s2egg doctor --root /path/to/server
```

For a production configuration, provide `STEAM_ACC` instead of opting into tokenless mode. `config check` validates settings; `config show` prints effective JSON with GSLT and webhook secrets replaced by `[REDACTED]`. Runtime fields retain the real secrets. The diagnostic output includes derived root/path values and is not a configuration export for round-tripping. Both whole configurations and individual Steam/webhook groups redact secrets when marshaled or formatted.

`doctor` validates settings and checks persistent SteamCMD/image-seed availability, `game/cs2.sh`, the executable `game/bin/linuxsteamrt64/cs2`, and the Metamod/S2Script directories under `game/csgo/addons`. It does not execute these programs, validate their compatibility, assess game readiness, install anything, or contact a webhook. Exit codes are `0` for valid configuration or present prerequisites, `1` for invalid configuration/missing prerequisites, and `2` for CLI usage errors. An empty server directory therefore produces missing-prerequisite diagnostics, not a readiness claim.

## Configuration

Precedence is built-in defaults, then optional `<root>/egg/server.json`, then explicitly present environment variables. Even an explicitly present empty value overrides a saved scalar and must pass validation. Root is the caller's `--root` argument, resolved to an existing absolute directory; environment/JSON cannot replace it. Missing configuration files are not created by diagnostics.

JSON uses schema version `1`, strict typed values, and the groups shown below. Fields may be omitted to retain defaults. Unknown fields, JSON null values, unsupported schema versions, trailing data, and files over 1 MiB are rejected. Do not commit secret-bearing operator configuration.

```json
{
  "schema_version": 1,
  "identity": "",
  "steam": {
    "appid": 730,
    "gslt": "",
    "allow_tokenless": true,
    "update": true,
    "validate": false
  },
  "frameworks": {
    "s2script_update": true,
    "metamod_update": true,
    "channel": "stable",
    "s2script_version": "latest",
    "metamod_version": "latest",
    "hud": false
  },
  "plugins": {
    "file": "s2script-plugins.json",
    "bundled": { "@s2script/basechat": false }
  },
  "launch": {
    "ip": "0.0.0.0",
    "port": 27015,
    "map": "de_dust2",
    "max_players": 32,
    "game_type": 0,
    "game_mode": 0,
    "debug_port": 0,
    "custom_params": "+hostname \"My server\""
  },
  "readiness": { "timeout_seconds": 120, "poll_interval_seconds": 1 },
  "webhook": {
    "url": "",
    "events": ["server.up", "server.down", "server.crash", "server.start_failed"],
    "timeout_seconds": 10,
    "queue_capacity": 64,
    "max_retries": 2
  }
}
```

This example explicitly opts into tokenless mode for local diagnostics and disables basechat. The default `allow_tokenless` is **false**. In JSON, boolean values must be `true`/`false`; panel boolean overrides must be `0`/`1`.

| Environment variable | JSON field | Default / validation |
| --- | --- | --- |
| `SERVER_ID` | `identity` | Empty; optional operator identity; otherwise runtime persists a standalone identity in `egg/server-id` |
| `SERVER_IP` | `launch.ip` | `0.0.0.0`; IPv4/IPv6 address; panel allocation value |
| `SERVER_PORT` | `launch.port` | `27015`; integer 1–65535; panel allocation value |
| `SRCDS_APPID` | `steam.appid` | `730`; immutable CS2 app ID |
| `SRCDS_MAP` | `launch.map` | `de_dust2`; nonempty map name |
| `SRCDS_MAXPLAYERS` | `launch.max_players` | `32`; integer 1–64 |
| `GAME_TYPE` / `GAME_MODE` | `launch.game_type` / `launch.game_mode` | `0`; nonnegative integers |
| `STEAM_ACC` | `steam.gslt` | Empty secret; required unless tokenless is explicitly enabled |
| `ALLOW_TOKENLESS` | `steam.allow_tokenless` | `0`; explicit `0` or `1` |
| `SRCDS_STOP_UPDATE` | `steam.update` (inverted) | `0`; `1` sets update false |
| `SRCDS_VALIDATE` | `steam.validate` | `0`; explicit `0` or `1` |
| `CUSTOM_PARAMS` | `launch.custom_params` | Empty; parsed into arguments using quotes/backslashes, with no shell expansion |
| `S2SCRIPT_UPDATE` / `METAMOD_UPDATE` | `frameworks.s2script_update` / `frameworks.metamod_update` | `1`; explicit `0` or `1`; startup policy |
| `S2SCRIPT_CHANNEL` | `frameworks.channel` | `stable`; `stable` or `prerelease` |
| `S2SCRIPT_VERSION` / `METAMOD_VERSION` | `frameworks.s2script_version` / `frameworks.metamod_version` | `latest`; or exact version such as `v0.8.1` / `2.0.0-git1371`; URLs/ranges rejected; verified compatibility resolution |
| `S2SCRIPT_REVIEWED_ONLY` | `plugins.reviewed_only` | `0` (default) or `1`; require exact registry review state `reviewed` |
| `S2SCRIPT_PLUGIN_FILE` | `plugins.file` | `s2script-plugins.json`; relative file path within root, including existing symlink resolution |
| `S2SCRIPT_HUD` | `frameworks.hud` | `0`; explicit `0` or `1`; optional staged native HUD setup |
| `GDB_DEBUG_PORT` | `launch.debug_port` | `0` disables; integer 1–65535 distinct from game port; requires debug image and restricted allocated TCP access |
| `DISCORD_WEBHOOK_URL` | `webhook.url` | Empty secret; HTTPS Discord `/api[/vN]/webhooks/<numeric-id>/<token>` URL when configured |
| `S2SCRIPT_PLUGIN_<NAME>` | `plugins.bundled` | `default`, `0`, or `1`; see [generated plugin controls](docs/bundled-plugins.md) |

Webhook hostnames are restricted to `discord.com`, `discordapp.com`, `canary.discord.com`, and `ptb.discord.com`; credentials, queries, and fragments in URLs are rejected. Readiness timeout accepts 1–3600 seconds; polling accepts 1 second through that timeout. Webhook timeout accepts 1–60 seconds. Additional selectable event names are `server.restart`, `server.update`, `backup`, and `guard`. Queue capacity accepts 1–1024 and max retries accepts 0–10 (defaults 64/2). Lifecycle delivery is wired now; restart/update/backup/guard producers remain future work. See [delivery limits and node ownership](docs/webhooks.md).

Panel plugin `default` preserves saved enablement or the bundled release default. Explicit panel `0`/`1` overrides that choice. Every installed plugin, including disabled, pinned, local, and missing entries, is checked during startup reconciliation; disabled artifacts remain outside the runtime's top-level scan. See [the plugin operator guide](docs/plugins.md).

`CUSTOM_PARAMS` is parsed as argument data, with no constructed shell command or substitution. Unbalanced quotes, a trailing escape, and NUL bytes are rejected. Runtime rejects custom overrides of managed allocation and GSLT arguments; unrelated arguments remain literal. See [reserved arguments and launch behavior](docs/runtime.md).

The executable now supports `s2egg prepare`, `s2egg run`, and `s2egg status`, each with `--root PATH`. These use the image/SteamCMD prerequisites described in [runtime contracts](docs/runtime.md). `status` reports the live managed owner, readiness, known/unknown player count, versions, and intended stop reason.

## Standalone egg import

The generated file is [pterodactyl/s2script-cs2-egg.json](pterodactyl/s2script-cs2-egg.json). Import it through the panel's egg import UI after building its configured image on the assigned node. Pterodactyl's reserved `SERVER_IP` and `SERVER_PORT` come from the assigned allocation and are deliberately absent from editable egg-variable definitions. Use the assigned port and a reachable game/UDP allocation for runtime readiness and client traffic.

The egg declares `s2egg run`, graceful stop command `quit`, a readiness-gated `S2EGG_READY` startup marker, and a first-start installation notice. It includes 18 unique inherited plugin controls: 14 release-enabled and four release-disabled. Maintainers should replace the placeholder `noreply@example.com` author on release. Panel import and live Pelican/Pterodactyl acceptance have not been exercised here.

To verify the saved artifacts without rewriting them:

```sh
go run ./cmd/egggen --check --catalog-output docs/bundled-plugins.md
```

The server is intended to run as its non-root container user. It requires no Docker socket or Wings administrator credential mounted into the game container. Optional node management, shared updates, backups, workshop management and guard features remain planned. The local A2S query gates `server.up`; process spawn alone does not announce success. This query does not prove native plugins loaded. Delivery after SIGKILL or host/power failure cannot be guaranteed by the standalone process.

## License

Project code is GPL-3.0-or-later. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Dependencies and downloaded game/framework artifacts retain their own licenses.

## Credits

[K4ryuu's CS2-Egg-Go](https://github.com/K4ryuu/CS2-Egg-Go) supplied the operational feature/parity reference and GPL licensing basis. [S2Script](https://github.com/s2script/s2script) supplied the framework and bundled-plugin identity/default reference (inspected revision `86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7`). [Metamod:Source](https://www.sourcemm.net/) is S2Script's native dependency. Valve provides CS2 and SteamCMD. No upstream product source module is imported; the Docker rootfs re-export approach is credited in NOTICE.

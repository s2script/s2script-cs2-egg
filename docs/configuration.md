# Configuration

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
| `S2EGG_BIND_IP` | `launch.ip` | `0.0.0.0`; optional explicit container-local IPv4/IPv6 bind address |
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
| `S2SCRIPT_VERSION` / `METAMOD_VERSION` | `frameworks.s2script_version` / `frameworks.metamod_version` | `latest`; or exact version such as `v0.8.1` / `2.0.0-git1476`; URLs/ranges rejected; verified compatibility resolution |
| `S2SCRIPT_REVIEWED_ONLY` | `plugins.reviewed_only` | `0` (default) or `1`; require exact registry review state `reviewed` |
| `S2SCRIPT_PLUGIN_FILE` | `plugins.file` | `s2script-plugins.json`; relative file path within root, including existing symlink resolution |
| `S2SCRIPT_HUD` | `frameworks.hud` | `0`; explicit `0` or `1`; optional staged native HUD setup |
| `GDB_DEBUG_PORT` | `launch.debug_port` | `0` disables; integer 1–65535 distinct from game port; requires debug image and restricted allocated TCP access |
| `DISCORD_WEBHOOK_URL` | `webhook.url` | Empty secret; HTTPS Discord `/api[/vN]/webhooks/<numeric-id>/<token>` URL when configured |
| `S2SCRIPT_PLUGIN_<NAME>` | `plugins.bundled` | `default`, `0`, or `1`; see [generated plugin controls](bundled-plugins.md) |

`SERVER_IP` is the panel's external allocation and does not override `launch.ip`.
With Docker bridge networking that address usually belongs to the host, not the
container. Leave the bind address at `0.0.0.0` for normal panel use; `SERVER_PORT`
still selects the allocated game port. Direct/host-network operators who formerly
used `SERVER_IP` to select a local interface must use `S2EGG_BIND_IP` or saved
`launch.ip` instead. The public allocation and firewall remain panel/node concerns.

Webhook hostnames are restricted to `discord.com`, `discordapp.com`, `canary.discord.com`, and `ptb.discord.com`; credentials, queries, and fragments in URLs are rejected. Readiness timeout accepts 1–3600 seconds; polling accepts 1 second through that timeout. Webhook timeout accepts 1–60 seconds. Additional selectable event names are `server.restart`, `server.update`, `backup`, and `guard`. Queue capacity accepts 1–1024 and max retries accepts 0–10 (defaults 64/2). Lifecycle and operational delivery are implemented by their corresponding enabled modules. See [delivery limits and node ownership](webhooks.md).

Panel plugin `default` preserves saved enablement or the bundled release default. Explicit panel `0`/`1` overrides that choice. Every installed plugin, including disabled, pinned, local, and missing entries, is checked during startup reconciliation; disabled artifacts remain outside the runtime's top-level scan. See [the plugin operator guide](plugins.md).

`CUSTOM_PARAMS` is parsed as argument data, with no constructed shell command or substitution. Unbalanced quotes, a trailing escape, and NUL bytes are rejected. Runtime rejects custom overrides of managed allocation and GSLT arguments; unrelated arguments remain literal. See [reserved arguments and launch behavior](runtime.md).

The executable now supports `s2egg prepare`, `s2egg run`, and `s2egg status`, each with `--root PATH`. These use the image/SteamCMD prerequisites described in [runtime contracts](runtime.md). `status` reports the live managed owner, readiness, known/unknown player count, versions, and intended stop reason.


See [examples/server.json](../examples/server.json) for a minimal tokenless diagnostic example. For production, supply a GSLT and turn off tokenless mode. Copy the file to egg/server.json; never save secret-bearing configuration to Git.

Bundled pins can constrain the compatible S2Script native generation; a newer runtime must not silently overwrite a pin. See [plugins](plugins.md) and [frameworks/HUD](frameworks.md). The exact list of all 18 controls is generated in [bundled-plugins.md](bundled-plugins.md). Four upstream defaults begin disabled: nominations, rockthevote, nextmap and funvotes.

Optional node settings live in a separate host-only node.json. Start from [examples/node.json](../examples/node.json), replace server IDs, paths, peer UIDs and access GIDs, and provision permissions from [node.md](node.md). Helper examples describe structural policy only; passing example validation does not prove runtime ownership, root-owned ancestors, mount isolation or service readiness. The node config-check and doctor commands check additional local prerequisites without starting services.

# Panel variables

Everything the panel shows on the Startup tab, what it does, and when you would change it. All of it is optional except the ones the panel marks required.

## Server basics

| Variable | Default | What it does |
|---|---|---|
| `SRCDS_MAP` | `de_dust2` | The map the server starts on |
| `SRCDS_MAXPLAYERS` | `32` | Slots. The panel caps it at 64 |
| `GAME_TYPE` / `GAME_MODE` | `0` / `0` | Casual, competitive, deathmatch and so on. `0/0` is casual |
| `CUSTOM_PARAMS` | empty | Extra arguments appended to the command line |
| `CONSOLE_LOG_LEVEL` | empty | `DEBUG`, `INFO`, `WARNING` or `ERROR`. Overrides `logging.json`'s `console_level` when set, empty uses the file's value |

## Steam

| Variable | Default | What it does |
|---|---|---|
| `STEAM_ACC` | empty | Your GSLT. Without one the server is not publicly listed. [Get one](https://steamcommunity.com/dev/managegameservers) |
| `ALLOW_TOKENLESS` | `0` | Lets the server run with no GSLT, LAN style. Set it when you know you want that |
| `SRCDS_STOP_UPDATE` | `0` | Skip the SteamCMD update on boot. Useful while debugging, not in production |
| `SRCDS_VALIDATE` | `0` | Run SteamCMD with `validate`. Slow, and it can overwrite files you edited. The console warns and waits five seconds before it starts |
| `SRCDS_APPID` | `730` | Do not touch |

The GSLT is masked in every console line and every log file, so a screenshot cannot leak it.

## Addons and plugins

Metamod, S2Script, MultiAddonManager and GameFixes default to enabled. Each selected addon checks updates on startup.

| Variable | Default | Purpose |
|---|---|---|
| `INSTALL_METAMOD` | `1` | Also required automatically by selected addons |
| `INSTALL_S2SCRIPT` | `1` | Framework and plugin preparation |
| `S2SCRIPT_CHANNEL` | `stable` | `stable` or `prerelease` framework releases |
| `S2SCRIPT_VERSION` | empty | Optional exact framework tag |
| `INSTALL_MULTIADDONMANAGER` | `1` | Linux SteamRT3 downloader |
| `MULTIADDONMANAGER_VERSION` | empty | Optional exact release tag |
| `INSTALL_GAMEFIXES` | `1` | GameFixes downloader |
| `GAMEFIXES_VERSION` | empty | Optional exact release tag |
| `PRERELEASE` | `0` | Include prereleases for Metamod/native lookups |
| 18 `S2SCRIPT_PLUGIN_*` switches | `default` | Preserve choices, `1` enable, `0` disable |

Exact switch names are in the egg. See [addons and plugin commands](../addons.md) and [node addon cache](../modules/addon-cache.md).

## Features

| Variable | Default | Turn it on when |
|---|---|---|
| `ENABLE_GUARD` | `0` | Advertising or console-spam bots bother you. Starts in log-only mode. [Guard](../modules/guard.md) |
| `GUARD_ACTION` | empty | `log`, `kick` or `block`. Overrides `guard.json`'s `action` when set, empty uses the file's value |
| `GUARD_BAN_MINUTES` | empty | Block length in minutes. Overrides `guard.json`'s `ban_minutes` when set, empty uses the file's value |
| `GUARD_WHITELIST_STEAMIDS` | empty | Comma-separated SteamID64s Bot Guard never acts on. Overrides `guard.json`'s `whitelist_steamids` when set |
| `GUARD_WHITELIST_IPS` | empty | Comma-separated IPs Bot Guard never acts on. Overrides `guard.json`'s `whitelist_ips` when set |
| `ENABLE_FILTER` | `0` | The console is too noisy. Rules in `egg/configs/console-filter.json` |
| `CLEANUP_ENABLED` | `0` | Demos, dumps and logs fill the disk. Rules in `egg/configs/cleanup.json`, unless the node runs the [cleanup module](../modules/cleanup.md) |
| `ENABLE_LOG_FILES` | `0` | Keep the console tab's history past a restart. Needs the node's [console-log module](../modules/console-log.md); on without it, one warning line and nothing is saved, it never writes inside the container |
| `ADDON_MIRRORS` | empty | Comma-separated `https://` prefixes to try when a GitHub download fails. Off by default: what a mirror returns is loaded as native code and there is nothing to verify it against |

## Development

Connection diagnostics are admin-only and available in both egg variants:

| Variable | Default | What it does |
|---|---|---|
| `ENABLE_CONNECTION_TRACE` | `0` | Opt in to a bounded read-only trace after restarting |
| `CONNECTION_TRACE_IP` | empty | One client source IP as seen by CS2; required when tracing |
| `CONNECTION_TRACE_SECONDS` | `600` | Capture window, 60–1800 seconds; restart renews it while enabled |

See [connection tracing](../modules/connection-trace.md) for comparison steps,
redaction scope and limits of node traffic evidence. It does not enable Guard.

| Variable | Default | What it does |
|---|---|---|
| `GDB_DEBUG_PORT` | empty | Runs the server under `gdbserver` on that port for remote debugging. Costs 5 to 10 percent performance. Allocate the port in the panel first |

Attach with IDA: Debugger > Remote GDB debugger, or `gdb` with `target remote <ip>:<port>`. Leave it empty on anything you care about.

---

[Docs index](../README.md)

## Rotating join passwords

| Variable | Default | Meaning |
|---|---|---|
| `ENABLE_ROTATING_PASSWORD` | `0` | Opt this server into the node's passwords module; restart after changing |

The node controls shared/unique mode and regeneration interval. See
[rotating passwords](../modules/passwords.md), including initial-enrollment and
node-outage behavior.

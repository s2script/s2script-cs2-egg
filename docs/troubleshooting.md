# Troubleshooting

Start with `s2egg version`, `s2egg config check --root PATH` and
`s2egg doctor --root PATH`. Supply the same environment as the server; diagnostics
do not invent a GSLT. `config show` redacts known secrets but still contains
deployment paths. Node diagnostics are `s2node doctor --config PATH --json`.
They inspect local prerequisites, not gameplay or stopped-state proof.

| Symptom | Check |
| --- | --- |
| First start is slow | SteamCMD/game download progress, free disk, writable HOME, outbound DNS/HTTPS/Steam access; framework preparation precedes game readiness |
| Image cannot be pulled | Correct immutable tag, completed public-verification job and GHCR package visibility; otherwise build from source |
| No `S2EGG_READY` or up event | Actual local A2S response, allocation, game exit/readiness timeout; a banner alone is insufficient |
| Metamod/S2Script fails to load | Native pair compatibility and game build; inspect logs and `meta list`; see [frameworks](frameworks.md) |
| Disabled plugin re-enables | Check explicit panel `0/1` overrides before manifest enablement; `default` inherits |
| Plugin update refuses | Exact pin/runtime coupling, identity collisions, reviewed-only policy, unsafe/manual ownership or incomplete publisher evidence; do not delete locks/cache to bypass verification |
| Registry unavailable | Only compatible verified retained artifacts can be used; a missing pinned package cannot be fabricated |
| Node says unknown/lost contact | Private socket mount, peer UID/GID, module policy and runtime reports; unknown never means stopped |
| Shared startup refuses fallback | Mount/generation identity or retained reference mismatch; use documented stopped standalone conversion, never run SteamCMD into a shared target |
| Backup/restore is refused | Runtime lease, actual-UID helper authority, archive hash, preview identities, disk quota and retained recovery catalogs |
| Duplicate/missing notifications | Event IDs, node ownership, configured filters, queue/retry/archive status; Discord transport cannot guarantee exactly once after ambiguous failure |
| Project update will not activate | Managed launcher layout, root-owned policy, version/source identity, protocol/state/helper compatibility and previous failure journal |
| Guard does not block | Opt-in block policy, exact external allocation, allowlists, helper health and nftables capability; log mode has no packet effect |
| Debug image times out | Attach and continue the debugger within readiness budget; keep its port private |

## Panel startup and CS2-Egg-Go

The panel install script intentionally only prints a message. Like
[CS2-Egg-Go's egg](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/pterodactyl/kitsunelab-cs2-go-egg.json),
the runtime downloads game files on first **Start**, into the persistent server
volume. Reinstalling the egg is not needed to populate that script.

| Operation | S2Script CS2 | CS2-Egg-Go |
| --- | --- | --- |
| Panel installation | Import JSON by URL; first start downloads CS2 and frameworks | Import JSON by URL; first start performs downloads |
| Game bind | `0.0.0.0` and allocated `SERVER_PORT`; optional explicit local bind | Default startup uses `+ip 0.0.0.0` and allocated port |
| Download console | Launcher banner, preparation stages and redacted SteamCMD output | SteamCMD output is streamed to its logger |
| Steam token | Requires `STEAM_ACC` unless `ALLOW_TOKENLESS=1` is explicit | Missing GSLT does not block launcher startup |
| Native frameworks | Requires a verified compatible Metamod/S2Script installation; preparation errors stop launch | Framework installations are optional switches |
| Startup command | Managed argument vector; extra arguments use `CUSTOM_PARAMS` | Expands the panel's `STARTUP` command |
| Panel ready marker | `S2EGG_READY`, only after a successful local A2S game query | Matches `Source2Init OK` from game output |

This comparison is based on the upstream egg and
[SteamCMD adapter](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/internal/egg/steamcmd/steamcmd.go)
inspected on 2026-10-13. Neither ready marker proves native plugin functionality.

The initial `v0.0.1-alpha.1` image hid SteamCMD output and used `SERVER_IP` as
the bind address. That could make downloads appear stalled, then reject the
host allocation as unavailable inside a bridge-networked container. The source
fixes documented here require a newer image release; updating the egg JSON
alone does not update an already-created runtime container.

If the panel shows only its own **server marked as starting** message, then goes
offline, that message does not establish that the launcher ran. With the updated
image, expect **Starting S2Script CS2** followed by a stage or configuration
error. If even that banner is absent, inspect Wings/container logs on the node
for image-pull, container-create, permission, architecture or process-exit errors.
Check the actual running image ID/version after pulling and recreating the
container; a moving `:prerelease` label alone does not prove which build ran.

If a stage is visible, use its error and SteamCMD exit code to diagnose it.
Provide `STEAM_ACC`, or deliberately enable `ALLOW_TOKENLESS=1` for tokenless
testing. The game readiness timeout begins after preparation and process launch;
it is not a limit on the initial CS2 download. Do not delete the server volume to
retry a startup failure.

Read [logs](logging.md), [cleanup/delivery evidence](cleanup.md),
[update recovery](updates.md) and [backup recovery](backups.md) before deleting
state. Use stable request IDs for retries and inspect uncertain outcomes.

For an issue, include source/version, panel and OS versions, selected modules,
expected/actual behavior and a minimal redacted reproducer. Exclude GSLTs,
webhook URLs, Wings credentials, private player data and binary dumps. Follow
[security reporting](../.github/SECURITY.md) for vulnerabilities.

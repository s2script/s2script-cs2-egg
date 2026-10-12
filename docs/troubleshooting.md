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

Read [logs](logging.md), [cleanup/delivery evidence](cleanup.md),
[update recovery](updates.md) and [backup recovery](backups.md) before deleting
state. Use stable request IDs for retries and inspect uncertain outcomes.

For an issue, include source/version, panel and OS versions, selected modules,
expected/actual behavior and a minimal redacted reproducer. Exclude GSLTs,
webhook URLs, Wings credentials, private player data and binary dumps. Follow
[security reporting](../.github/SECURITY.md) for vulnerabilities.
